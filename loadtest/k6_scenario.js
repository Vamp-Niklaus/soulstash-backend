import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '30s', target: 50 },  // Ramp up to 50 users
    { duration: '1m',  target: 100 }, // Steady state 100 concurrent users
    { duration: '30s', target: 200 }, // Spike to 200 users
    { duration: '30s', target: 0 },   // Ramp down to 0
  ],
  thresholds: {
    http_req_duration: ['p(95)<150', 'p(99)<300'], // 95% of requests under 150ms, 99% under 300ms
    http_req_failed: ['rate<0.01'],                 // Error rate < 1%
  },
};

const BASE_URL = __ENV.TARGET_URL || 'http://localhost:5000';

export default function () {
  // 1. Health check
  const healthRes = http.get(`${BASE_URL}/api/health`);
  check(healthRes, {
    'health status 200': (r) => r.status === 200,
  });

  // 2. Movie detail with rating enrichment
  const movieRes = http.get(`${BASE_URL}/movies/550`);
  check(movieRes, {
    'movie status 200': (r) => r.status === 200,
  });

  // 3. Image proxy caching
  const imgRes = http.get(`${BASE_URL}/image/sample.jpg`);
  check(imgRes, {
    'image status 200 or 404': (r) => r.status === 200 || r.status === 404,
  });

  sleep(1);
}
