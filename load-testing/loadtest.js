import crypto from 'k6/crypto';
import http   from 'k6/http';
import { check, sleep } from 'k6';

// ─── Config ───────────────────────────────────────────────────────────────────
const ENDPOINT   = (__ENV.MINIO_ENDPOINT  || 'http://localhost:9000').replace(/\/+$/, '');
const ACCESS_KEY = __ENV.MINIO_ACCESS_KEY || '';
const SECRET_KEY = __ENV.MINIO_SECRET_KEY || '';
const REGION     = __ENV.MINIO_REGION     || 'us-east-1';
const BUCKET     = __ENV.MINIO_BUCKET_NAME || 'images-raw-classic';
const PREFIX     = __ENV.K6_OBJECT_PREFIX  || 'benchmark';
const IMAGE_FILE = __ENV.IMAGE_FILE        || './light-smoke.png';

// Opened once at init — shared across all VUs (read-only)
const imageBytes = open(IMAGE_FILE, 'b');

// Host header value extracted from endpoint (e.g. "130.61.146.183:9000")
const HOST = ENDPOINT.replace(/^https?:\/\//, '').split('/')[0];

export const options = {
  discardResponseBodies: false,
  scenarios: {
    load: {
      executor: 'constant-vus',
      vus:      Number(__ENV.K6_VUS      || '5'),
      duration: __ENV.K6_DURATION        || '30s',
    },
  },
};

// ─── AWS Signature V4 ─────────────────────────────────────────────────────────
function hmac256(key, data) {
  // key: string | ArrayBuffer, data: string | ArrayBuffer → ArrayBuffer
  return crypto.hmac('sha256', key, data, 'binary');
}

function sha256hex(data) {
  return crypto.sha256(data, 'hex');
}

function isoTimestamp() {
  // Returns "20230101T120000Z"
  return new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
}

function deriveSigningKey(secretKey, dateStamp) {
  const kDate    = hmac256('AWS4' + secretKey, dateStamp);
  const kRegion  = hmac256(kDate,    REGION);
  const kService = hmac256(kRegion,  's3');
  return         hmac256(kService, 'aws4_request');
}

function signedHeaders(objectPath, body) {
  const amzDate   = isoTimestamp();
  const dateStamp = amzDate.slice(0, 8);

  const payloadHash = sha256hex(body);

  // Headers must be sorted alphabetically
  const canonicalHeaders =
    `content-type:image/png\n` +
    `host:${HOST}\n` +
    `x-amz-content-sha256:${payloadHash}\n` +
    `x-amz-date:${amzDate}\n`;
  const signedHeaderNames = 'content-type;host;x-amz-content-sha256;x-amz-date';

  const canonicalRequest = [
    'PUT',
    objectPath,
    '',   // no query string
    canonicalHeaders,
    signedHeaderNames,
    payloadHash,
  ].join('\n');

  const credentialScope = `${dateStamp}/${REGION}/s3/aws4_request`;
  const stringToSign = [
    'AWS4-HMAC-SHA256',
    amzDate,
    credentialScope,
    sha256hex(canonicalRequest),
  ].join('\n');

  const signingKey = deriveSigningKey(SECRET_KEY, dateStamp);
  const signature  = crypto.hmac('sha256', signingKey, stringToSign, 'hex');

  return {
    'Authorization':       `AWS4-HMAC-SHA256 Credential=${ACCESS_KEY}/${credentialScope}, SignedHeaders=${signedHeaderNames}, Signature=${signature}`,
    'Content-Type':        'image/png',
    'x-amz-date':          amzDate,
    'x-amz-content-sha256': payloadHash,
  };
}

// ─── Test function ────────────────────────────────────────────────────────────
export default function () {
  const key        = `${PREFIX}/${__VU}/${Date.now()}-${Math.floor(Math.random() * 1e6)}.png`;
  const objectPath = `/${BUCKET}/${key}`;
  const url        = `${ENDPOINT}${objectPath}`;

  const res = http.put(url, imageBytes, {
    headers: signedHeaders(objectPath, imageBytes),
  });

  check(res, {
    'upload ok': (r) => r.status === 200 || r.status === 201,
  });

  sleep(1);
}
