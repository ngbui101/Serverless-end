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
	NATSURL         string
	NATSSubject     string
	NATSStream      string
	NATSDurable     string
	NATSBatchSize   int
	NATSFetchWait   time.Duration
	MinIOEndpoint   string
	MinIOAccessKey  string
	MinIOSecretKey  string
	RawBucket       string
	ProcessedBucket string
	MinIOPublicURL  string
	PostgresHost    string
	PostgresPort    string
	PostgresDB      string
	PostgresUser    string
	PostgresPass    string
	PostgresSSLMode string
	HealthAddr      string
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

type worker struct {
	cfg   config
	minio *minio.Client
	db    *sql.DB
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	minioClient, err := newMinIOClient(cfg)
	if err != nil {
		log.Fatalf("minio client error: %v", err)
	}

	db, err := openDB(cfg)
	if err != nil {
		log.Fatalf("postgres error: %v", err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go serveHealth(cfg.HealthAddr)

	w := &worker{cfg: cfg, minio: minioClient, db: db}
	if err := w.run(ctx); err != nil {
		log.Fatalf("worker stopped: %v", err)
	}
	log.Printf("worker stopped gracefully")
}

func loadConfig() (config, error) {
	cfg := config{
		NATSURL:         getenv("NATS_URL", "nats://nats.serverless.svc.cluster.local:4222"),
		NATSSubject:     getenv("NATS_SUBJECT", "minio.images.raw.serverless"),
		NATSStream:      getenv("NATS_STREAM", "MINIO_RAW_SERVERLESS"),
		NATSDurable:     getenv("NATS_DURABLE", "image-process-worker"),
		NATSBatchSize:   getenvInt("NATS_BATCH_SIZE", 1),
		NATSFetchWait:   time.Duration(getenvInt("NATS_FETCH_TIMEOUT_MS", 2000)) * time.Millisecond,
		MinIOEndpoint:   os.Getenv("MINIO_ENDPOINT"),
		MinIOAccessKey:  os.Getenv("MINIO_ACCESS_KEY"),
		MinIOSecretKey:  os.Getenv("MINIO_SECRET_KEY"),
		RawBucket:       getenv("MINIO_BUCKET_RAW", "images-raw-serverless"),
		ProcessedBucket: getenv("MINIO_BUCKET_PROCESSED", "images-processed"),
		MinIOPublicURL:  os.Getenv("MINIO_PUBLIC_URL"),
		PostgresHost:    os.Getenv("POSTGRES_HOST"),
		PostgresPort:    getenv("POSTGRES_PORT", "5432"),
		PostgresDB:      os.Getenv("POSTGRES_DB"),
		PostgresUser:    os.Getenv("POSTGRES_USER"),
		PostgresPass:    os.Getenv("POSTGRES_PASSWORD"),
		PostgresSSLMode: getenv("POSTGRES_SSLMODE", "disable"),
		HealthAddr:      getenv("HEALTH_ADDR", ":8080"),
	}

	var missing []string
	for name, value := range map[string]string{
		"MINIO_ENDPOINT":    cfg.MinIOEndpoint,
		"MINIO_ACCESS_KEY":  cfg.MinIOAccessKey,
		"MINIO_SECRET_KEY":  cfg.MinIOSecretKey,
		"MINIO_PUBLIC_URL":  cfg.MinIOPublicURL,
		"POSTGRES_HOST":     cfg.PostgresHost,
		"POSTGRES_DB":       cfg.PostgresDB,
		"POSTGRES_USER":     cfg.PostgresUser,
		"POSTGRES_PASSWORD": cfg.PostgresPass,
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
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
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

func openDB(cfg config) (*sql.DB, error) {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.PostgresUser, cfg.PostgresPass),
		Host:   net.JoinHostPort(cfg.PostgresHost, cfg.PostgresPort),
		Path:   "/" + cfg.PostgresDB,
	}
	query := dsn.Query()
	query.Set("sslmode", cfg.PostgresSSLMode)
	dsn.RawQuery = query.Encode()

	dbConfig, err := pgx.ParseConfig(dsn.String())
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*dbConfig)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func serveHealth(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("health server stopped: %v", err)
	}
}

func (w *worker) run(ctx context.Context) error {
	nc, err := nats.Connect(w.cfg.NATSURL)
	if err != nil {
		return err
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	sub, err := js.PullSubscribe(
		w.cfg.NATSSubject,
		w.cfg.NATSDurable,
		nats.BindStream(w.cfg.NATSStream),
		nats.ManualAck(),
	)
	if err != nil {
		return err
	}

	log.Printf("image-process-worker subscribed subject=%s stream=%s durable=%s", w.cfg.NATSSubject, w.cfg.NATSStream, w.cfg.NATSDurable)
	for {
		select {
		case <-ctx.Done():
			log.Printf("shutdown requested, stopping fetch loop")
			return nil
		default:
		}

		messages, err := sub.Fetch(w.cfg.NATSBatchSize, nats.MaxWait(w.cfg.NATSFetchWait))
		if errors.Is(err, nats.ErrTimeout) {
			continue
		}
		if err != nil {
			return err
		}
		for _, msg := range messages {
			w.handle(context.Background(), msg)
		}
	}
}

func (w *worker) handle(ctx context.Context, msg *nats.Msg) {
	event, err := parseMinIOEvent(msg.Data)
	if err != nil {
		log.Printf("invalid minio event, acking poison message: %v", err)
		_ = msg.Ack()
		return
	}
	if event.Bucket != w.cfg.RawBucket {
		log.Printf("skipping bucket=%s key=%s", event.Bucket, event.Key)
		_ = msg.Ack()
		return
	}
	if err := w.process(ctx, event); err != nil {
		log.Printf("failed to process bucket=%s key=%s: %v", event.Bucket, event.Key, err)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
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

func (w *worker) process(ctx context.Context, event minioEvent) error {
	started := time.Now()
	imageID := uuid.New()
	originalFilename := path.Base(event.Key)
	processedKey := replaceExtension(event.Key, ".webp")

	webpBytes, err := w.convertToWebP(ctx, event.Bucket, event.Key)
	if err != nil {
		return err
	}

	_, err = w.minio.PutObject(ctx, w.cfg.ProcessedBucket, processedKey, bytes.NewReader(webpBytes), int64(len(webpBytes)), minio.PutObjectOptions{
		ContentType: "image/webp",
	})
	if err != nil {
		return err
	}

	processedURL := strings.TrimRight(w.cfg.MinIOPublicURL, "/") + "/" + w.cfg.ProcessedBucket + "/" + processedKey
	processingTimeMS := time.Since(started).Milliseconds()
	if err := w.insertProcessedImage(ctx, imageID, originalFilename, processedURL, processingTimeMS); err != nil {
		return err
	}

	if err := w.minio.RemoveObject(ctx, event.Bucket, event.Key, minio.RemoveObjectOptions{}); err != nil {
		return err
	}

	log.Printf("stored key=%s processed=%s image_id=%s processing_time_ms=%d", event.Key, processedKey, imageID, processingTimeMS)
	return nil
}

func (w *worker) convertToWebP(ctx context.Context, bucket, key string) ([]byte, error) {
	object, err := w.minio.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
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
	input, err := os.CreateTemp("", "image-process-worker-*.png")
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

	output, err := os.CreateTemp("", "image-process-worker-*.webp")
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

func (w *worker) insertProcessedImage(ctx context.Context, imageID uuid.UUID, originalFilename, processedURL string, processingTimeMS int64) error {
	_, err := w.db.ExecContext(
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
