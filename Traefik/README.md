# MiniSource Gateway — Traefik

جایگزین سبک برای gateway قدیمی (Go). همان روتینگ، بدون پیچیدگی.

```
Internet → :9000 (Traefik) → auth / storage / payment
                             ↓
                      :8080 (Dashboard)
```

## ساختار

```
Traefik/
├── docker-compose.yml   # سرویس Traefik
├── traefik.yml          # Static config (entrypoints, dashboard, ping)
├── dynamic/
│   └── routes.yml       # روت‌ها (hot-reload، بدون ری‌استارت)
└── README.md
```

## پیش‌نیازها

> سرویس‌های backend باید روی هاست در حال اجرا باشند:

| سرویس | پورت |
|-------|------|
| auth | `127.0.0.1:9001` |
| payment | `127.0.0.1:5010` |
| storage | `127.0.0.1:5030` |

> Traefik داخل کانتینر از طریق `host.docker.internal` به این پورت‌ها وصل می‌شود.

## اجرا

```bash
cd Traefik
docker compose up -d
```

- **API Gateway:** http://localhost:9000
- **Dashboard:** http://localhost:8080

توقف:

```bash
docker compose down
```

## روتینگ

| هاست | مسیر | → سرویس |
|------|------|---------|
| `localhost` / هر هاست | `/api/v1/*` | payment |
| `localhost` / هر هاست | `/v1/system/*`, `/result`, `/health`, `/swagger` | payment |
| `localhost` / هر هاست | `/api/v1/gateway-callbacks/*` | payment (public) |
| `localhost` / هر هاست | `/.well-known` | auth |
| `localhost` / هر هاست | `/v1/auth/*` | auth |
| `api.payment.com` | `/api/v1/*` | payment |
| `api.storage.com` | `/api/v1/*` | storage |

### معماری سرویس-به-سرویس

تماس‌های backend-to-backend مستقیم انجام می‌شوند (بدون عبور از gateway):

| مبدا | مقصد | روش |
|------|------|-----|
| Storage backend | Payment service | مستقیم (`host.docker.internal:5010`) |
| Payment backend | Notifier | مستقیم (`host.docker.internal:5020`) |
| Payment backend | Auth | مستقیم (`host.docker.internal:9001`) |

### هدرهای الزامی

Gateway هدرهای زیر را forward می‌کند و strip نمی‌کند:

```http
Authorization
Content-Type
X-Tenant-Id
X-Application-Code
X-Language
Accept-Language
X-Request-ID
X-Correlation-Id
Idempotency-Key
```

### مسیرهای عمومی (بدون احراز هویت)

- `/.well-known/*` — JWKS (auth)
- `/health`, `/ready`, `/live`, `/metrics` — Health (payment)
- `/v1/system/public-config` — Public config (payment)
- `/api/v1/gateway-callbacks/*` — Bank callbacks (payment)

> توجه: gateway فقط routing/CORS انجام می‌دهد. احراز هویت و اعتبارسنجی
> tenant در خود backendها انجام می‌شود. gateway منبع امنیت نیست.

> نکته امنیتی: bank gateway logic فقط در Payment service است.

> روت‌های development (بدون قید هاست) اولویت کمتری دارند و به‌عنوان
> fallback عمل می‌کنند — معادل `allowDevHostFallback` در gateway قدیمی.

## تست

```bash
# auth (نباید نیاز به توکن داشته باشد چون public‌ است)
curl http://localhost:9000/.well-known/jwks.json

# شبیه‌سازی دامنه production با Host header
curl -H "Host: api.storage.com" http://localhost:9000/api/v1/files
```

## تغییر روت‌ها

فایل `dynamic/routes.yml` را ویرایش کنید — Traefik به‌صورت خودکار (بدون
ری‌استارت) تغییرات را اعمال می‌کند. برای تغییر پورت‌ها یا entrypoint‌ها،
فایل `traefik.yml` را ویرایش و کانتینر را ری‌استارت کنید:

```bash
docker compose restart traefik
```

## افزودن سرویس جدید

۱. در `dynamic/routes.yml` یک service اضافه کنید:

```yaml
http:
  services:
    comment:
      loadBalancer:
        servers:
          - url: "http://host.docker.internal:5010"
```

۲. یک router اضافه کنید:

```yaml
  routers:
    dev-comment:
      rule: "PathPrefix(`/v1/comment`)"
      entryPoints: [web]
      service: comment
      middlewares: [cors]
      priority: 50
```

۳. فایل را ذخیره کنید — بدون ری‌استارت اعمال می‌شود.

## امنیت (برای پروداکشن)

در `traefik.yml`، `api.insecure: true` را به `false` تغییر دهید و یک
router امن با middleware احراز هویت (BasicAuth یا ForwardAuth) روی مسیر
داشبورد تعریف کنید.
