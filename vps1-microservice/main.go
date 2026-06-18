package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/nats-io/nats.go"
)

type config struct {
	AppPort                 string
	MinIOEndpoint           string
	MinIOAccessKey          string
	MinIOSecretKey          string
	RawBucket               string
	ProcessedBucket         string
	MinIOPublicURL          string
	PostgresDSN             string
	MaxConcurrentProcessing int
	NatsURL                 string
}

type minioEvent struct {
	Bucket string
	Key    string
}

type minioEventPayload struct {
	Records []struct {
		S3 struct {
			Bucket struct {
				Name string `json:"name"`
			} `json:"bucket"`
			Object struct {
				Key string `json:"key"`
			} `json:"object"`
		} `json:"s3"`
	} `json:"Records"`
}

type service struct {
	cfg             config
	minio           *minio.Client
	db              *sql.DB
	stats           metrics
	natsConn        *nats.Conn
}

type metrics struct {
	requestsTotal       atomic.Uint64
	acceptedTotal       atomic.Uint64
	skippedTotal        atomic.Uint64
	invalidTotal        atomic.Uint64
	rejectedTotal       atomic.Uint64
	failuresTotal       atomic.Uint64
	processedTotal      atomic.Uint64
	processingMillisSum atomic.Uint64
	processingInFlight  atomic.Int64
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	minioClient, err := newMinIOClient(cfg)
	if err != nil {
		log.Fatalf("minio client error: %v", err)
	}
	db, err := openDB(cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("postgres error: %v", err)
	}
	defer db.Close()

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		log.Fatalf("nats connect error: %v", err)
	}
	defer nc.Close()

	svc := newService(cfg, minioClient, db, nc)
	svc.startNatsSubscriber()

	server := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           svc.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("vps1-microservice listening port=%s raw_bucket=%s processed_bucket=%s max_concurrent_processing=%d", cfg.AppPort, cfg.RawBucket, cfg.ProcessedBucket, cfg.MaxConcurrentProcessing)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server stopped: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func loadConfig() (config, error) {
	dsn, err := postgresDSNFromEnv()
	if err != nil {
		return config{}, err
	}
	cfg := config{
		AppPort:                 getenv("APP_PORT", "8080"),
		MinIOEndpoint:           os.Getenv("MINIO_ENDPOINT"),
		MinIOAccessKey:          os.Getenv("MINIO_ACCESS_KEY"),
		MinIOSecretKey:          os.Getenv("MINIO_SECRET_KEY"),
		RawBucket:               getenv("MINIO_BUCKET_RAW", "images-raw-classic"),
		ProcessedBucket:         getenv("MINIO_BUCKET_PROCESSED", "images-processed"),
		MinIOPublicURL:          strings.TrimRight(os.Getenv("MINIO_PUBLIC_URL"), "/"),
		PostgresDSN:             dsn,
		MaxConcurrentProcessing: getenvInt("VPS1_MAX_CONCURRENT_PROCESSING", 2),
		NatsURL:                 getenv("NATS_URL", "nats://vps1-nats:4222"),
	}

	var missing []string
	for name, value := range map[string]string{
		"MINIO_ENDPOINT":   cfg.MinIOEndpoint,
		"MINIO_ACCESS_KEY": cfg.MinIOAccessKey,
		"MINIO_SECRET_KEY": cfg.MinIOSecretKey,
		"MINIO_PUBLIC_URL": cfg.MinIOPublicURL,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing env vars: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func getenvInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func newService(cfg config, minioClient *minio.Client, db *sql.DB, nc *nats.Conn) *service {
	if cfg.MaxConcurrentProcessing <= 0 {
		cfg.MaxConcurrentProcessing = 2
	}
	return &service{
		cfg:      cfg,
		minio:    minioClient,
		db:       db,
		natsConn: nc,
	}
}

func newMinIOClient(cfg config) (*minio.Client, error) {
	endpoint, secure, err := parseMinIOEndpoint(cfg.MinIOEndpoint)
	if err != nil {
		return nil, err
	}
	return minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: secure,
	})
}

func parseMinIOEndpoint(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	if parsed.Scheme == "" {
		return raw, false, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("unsupported MINIO_ENDPOINT scheme %q", parsed.Scheme)
	}
	return parsed.Host, parsed.Scheme == "https", nil
}

func openDB(dsn string) (*sql.DB, error) {
	dbConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*dbConfig)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(2 * time.Minute)
	db.SetConnMaxLifetime(10 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func postgresDSNFromEnv() (string, error) {
	if springURL := strings.TrimSpace(os.Getenv("SPRING_DATASOURCE_URL")); springURL != "" {
		return postgresDSNFromSpring(springURL, os.Getenv("SPRING_DATASOURCE_USERNAME"), os.Getenv("SPRING_DATASOURCE_PASSWORD"), getenv("POSTGRES_SSLMODE", "disable"))
	}

	host := os.Getenv("POSTGRES_HOST")
	port := getenv("POSTGRES_PORT", "5432")
	dbName := os.Getenv("POSTGRES_DB")
	user := os.Getenv("POSTGRES_USER")
	password := os.Getenv("POSTGRES_PASSWORD")
	sslmode := getenv("POSTGRES_SSLMODE", "disable")
	var missing []string
	for name, value := range map[string]string{
		"POSTGRES_HOST":     host,
		"POSTGRES_DB":       dbName,
		"POSTGRES_USER":     user,
		"POSTGRES_PASSWORD": password,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing env vars: %s", strings.Join(missing, ", "))
	}
	return buildPostgresDSN(host, port, dbName, user, password, sslmode), nil
}

func postgresDSNFromSpring(raw, username, password, defaultSSLMode string) (string, error) {
	raw = strings.TrimPrefix(raw, "jdbc:")
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "postgresql" && parsed.Scheme != "postgres" {
		return "", fmt.Errorf("unsupported SPRING_DATASOURCE_URL scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	dbName := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if decoded, err := url.PathUnescape(dbName); err == nil {
		dbName = decoded
	}
	query := parsed.Query()
	sslmode := query.Get("sslmode")
	if sslmode == "" {
		sslmode = defaultSSLMode
	}
	if username == "" {
		username = query.Get("user")
	}
	if password == "" {
		password = query.Get("password")
	}
	var missing []string
	for name, value := range map[string]string{
		"postgres host":     host,
		"postgres database": dbName,
		"postgres username": username,
		"postgres password": password,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing %s in SPRING_DATASOURCE_URL config", strings.Join(missing, ", "))
	}
	return buildPostgresDSN(host, port, dbName, username, password, sslmode), nil
}

func buildPostgresDSN(host, port, dbName, username, password, sslmode string) string {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + dbName,
	}
	query := dsn.Query()
	query.Set("sslmode", sslmode)
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return mux
}

func (s *service) startNatsSubscriber() {
	eventChan := make(chan []byte, 100000)

	// Start worker pool
	for i := 0; i < s.cfg.MaxConcurrentProcessing; i++ {
		go func(workerID int) {
			for payload := range eventChan {
				s.stats.requestsTotal.Add(1)
				s.stats.processingInFlight.Add(1)
				
				event, err := parseMinIOEvent(payload)
				if err != nil {
					s.stats.invalidTotal.Add(1)
					log.Printf("invalid minio event from nats: %v", err)
				} else if event.Bucket != s.cfg.RawBucket {
					s.stats.skippedTotal.Add(1)
					log.Printf("skip bucket=%s expected_bucket=%s", event.Bucket, s.cfg.RawBucket)
				} else {
					s.stats.acceptedTotal.Add(1)
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					if err := s.process(ctx, event); err != nil {
						s.stats.failuresTotal.Add(1)
						log.Printf("process failed bucket=%s key=%s err=%v", event.Bucket, event.Key, err)
					}
					cancel()
				}
				s.stats.processingInFlight.Add(-1)
			}
		}(i)
	}

	_, err := s.natsConn.Subscribe("MINIO_RAW_CLASSIC", func(msg *nats.Msg) {
		select {
		case eventChan <- msg.Data:
			// Added to memory queue successfully
		default:
			s.stats.rejectedTotal.Add(1)
			log.Printf("warning: internal event channel full, dropping message")
		}
	})
	if err != nil {
		log.Fatalf("failed to subscribe to NATS: %v", err)
	}
	log.Println("NATS subscriber started on MINIO_RAW_CLASSIC")
}

func parseMinIOEvent(payload []byte) (minioEvent, error) {
	var event minioEventPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		return minioEvent{}, err
	}
	if len(event.Records) == 0 {
		return minioEvent{}, errors.New("missing Records")
	}
	bucket := event.Records[0].S3.Bucket.Name
	key, err := url.QueryUnescape(event.Records[0].S3.Object.Key)
	if err != nil {
		return minioEvent{}, err
	}
	if strings.TrimSpace(bucket) == "" || strings.TrimSpace(key) == "" {
		return minioEvent{}, errors.New("missing bucket or object key")
	}
	return minioEvent{Bucket: bucket, Key: key}, nil
}

func (s *service) process(ctx context.Context, event minioEvent) error {
	started := time.Now()
	imageID := uuid.New()
	originalFilename := path.Base(event.Key)
	processedKey := replaceExtension(event.Key, ".webp")

	webpBytes, err := s.convertToWebP(ctx, event.Bucket, event.Key)
	if err != nil {
		return err
	}
	if _, err := s.minio.PutObject(ctx, s.cfg.ProcessedBucket, processedKey, bytes.NewReader(webpBytes), int64(len(webpBytes)), minio.PutObjectOptions{
		ContentType: "image/webp",
	}); err != nil {
		return err
	}

	processingTimeMS := time.Since(started).Milliseconds()
	processedURL := s.cfg.MinIOPublicURL + "/" + s.cfg.ProcessedBucket + "/" + processedKey
	if err := s.insertProcessedImage(ctx, imageID, originalFilename, processedURL, processingTimeMS); err != nil {
		return err
	}
	if err := s.minio.RemoveObject(ctx, event.Bucket, event.Key, minio.RemoveObjectOptions{}); err != nil {
		return err
	}

	s.stats.processedTotal.Add(1)
	s.stats.processingMillisSum.Add(uint64(processingTimeMS))
	log.Printf("processed key=%s processed=%s image_id=%s processing_time_ms=%d", event.Key, processedKey, imageID, processingTimeMS)
	return nil
}

func (s *service) convertToWebP(ctx context.Context, bucket, key string) ([]byte, error) {
	object, err := s.minio.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()

	source, _, err := image.Decode(object)
	if err != nil {
		if errors.Is(err, image.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("unsupported image payload for %s: %w", key, err)
		}
		return nil, err
	}

	normalized := normalize(source)
	input, err := os.CreateTemp("", "vps1-microservice-*.png")
	if err != nil {
		return nil, err
	}
	inputPath := input.Name()
	defer os.Remove(inputPath)

	if err := png.Encode(input, normalized); err != nil {
		input.Close()
		return nil, err
	}
	if err := input.Close(); err != nil {
		return nil, err
	}

	output, err := os.CreateTemp("", "vps1-microservice-*.webp")
	if err != nil {
		return nil, err
	}
	outputPath := output.Name()
	output.Close()
	defer os.Remove(outputPath)

	cmd := exec.CommandContext(ctx, "cwebp", "-quiet", inputPath, "-o", outputPath)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cwebp failed: %w: %s", err, strings.TrimSpace(string(combined)))
	}
	return os.ReadFile(outputPath)
}

func normalize(source image.Image) image.Image {
	bounds := source.Bounds()
	normalized := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(normalized, normalized.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(normalized, normalized.Bounds(), source, bounds.Min, draw.Over)
	return normalized
}

func (s *service) insertProcessedImage(ctx context.Context, imageID uuid.UUID, originalFilename, processedURL string, processingTimeMS int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO processed_images (image_id, original_filename, minio_processed_url, processing_time_ms) VALUES ($1, $2, $3, $4)`,
		imageID,
		originalFilename,
		processedURL,
		processingTimeMS,
	)
	return err
}

func replaceExtension(key, extension string) string {
	ext := filepath.Ext(key)
	if ext == "" {
		return key + extension
	}
	return strings.TrimSuffix(key, ext) + extension
}

func (s *service) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	processed := s.stats.processedTotal.Load()
	sum := s.stats.processingMillisSum.Load()
	avg := float64(0)
	if processed > 0 {
		avg = float64(sum) / float64(processed)
	}
	_, _ = fmt.Fprintf(w, "# HELP vps1_webhook_requests_total Total Classic MinIO webhook requests.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_webhook_requests_total counter\nvps1_webhook_requests_total %d\n", s.stats.requestsTotal.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_webhook_accepted_total Successfully accepted Classic MinIO events.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_webhook_accepted_total counter\nvps1_webhook_accepted_total %d\n", s.stats.acceptedTotal.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_webhook_invalid_total Invalid Classic MinIO events.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_webhook_invalid_total counter\nvps1_webhook_invalid_total %d\n", s.stats.invalidTotal.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_webhook_skipped_total Classic MinIO events skipped because the bucket did not match.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_webhook_skipped_total counter\nvps1_webhook_skipped_total %d\n", s.stats.skippedTotal.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_processing_rejected_total Classic MinIO events rejected because all processing slots were busy.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processing_rejected_total counter\nvps1_processing_rejected_total %d\n", s.stats.rejectedTotal.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_processing_failures_total Failed Classic image processing attempts.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processing_failures_total counter\nvps1_processing_failures_total %d\n", s.stats.failuresTotal.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_processing_inflight Current Classic image processing operations.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processing_inflight gauge\nvps1_processing_inflight %d\n", s.stats.processingInFlight.Load())
	_, _ = fmt.Fprintf(w, "# HELP vps1_processing_concurrency_limit Maximum concurrent Classic image processing operations.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processing_concurrency_limit gauge\nvps1_processing_concurrency_limit %d\n", s.cfg.MaxConcurrentProcessing)
	_, _ = fmt.Fprintf(w, "# HELP vps1_processed_images_total Successfully processed Classic images.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processed_images_total counter\nvps1_processed_images_total %d\n", processed)
	_, _ = fmt.Fprintf(w, "# HELP vps1_processing_time_milliseconds_sum Sum of successful Classic image processing time in milliseconds.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processing_time_milliseconds_sum counter\nvps1_processing_time_milliseconds_sum %d\n", sum)
	_, _ = fmt.Fprintf(w, "# HELP vps1_processing_time_milliseconds_avg Average successful Classic image processing time in milliseconds.\n")
	_, _ = fmt.Fprintf(w, "# TYPE vps1_processing_time_milliseconds_avg gauge\nvps1_processing_time_milliseconds_avg %s\n", strconv.FormatFloat(avg, 'f', 3, 64))
}
