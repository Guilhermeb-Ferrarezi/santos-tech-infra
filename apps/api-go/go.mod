module github.com/santos-tech/auth

go 1.25.0

require (
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/alexedwards/argon2id v1.0.0
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/hibiken/asynq v0.26.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/klauspost/compress v1.19.1
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/pquerna/otp v1.5.0
	github.com/prometheus/client_golang v1.24.1
	github.com/redis/go-redis/v9 v9.22.0
	github.com/santos-tech/golog v0.0.0
	golang.org/x/oauth2 v0.36.0
)

replace github.com/santos-tech/golog => ../../packages/golog

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/boombuler/barcode v1.1.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/getsentry/sentry-go v0.48.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
