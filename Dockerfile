# --- Birinchi bosqich: Build (Kompilyatsiya) bosqichi ---
# Go ilovasini kompilyatsiya qilish uchun to'liq Go tasviridan foydalanamiz
FROM golang:1.24.3-alpine AS builder

# Konteyner ichida ish katalogini belgilash
WORKDIR /app

# Go modullari fayllarini nusxalash va dependencylarni yuklab olish
COPY go.mod .
COPY go.sum .
RUN go mod download

# Loyihaning asosiy Go kodini nusxalash
# Faqat main.go va app/ katalogini nusxalaymiz
COPY . .

# Go ilovasini kompilyatsiya qilish
# Endi faqat asosiy paketni kompilyatsiya qilamiz
RUN CGO_ENABLED=0 GOOS=linux go build -o code-executor -ldflags "-s -w" .

# --- Ikkinchi bosqich: Yakuniy (Runtime) bosqichi ---
# Kompilyatsiya qilingan binaryni ishga tushirish uchun minimal Alpine tasviridan foydalanamiz
FROM alpine:latest

# MUHIM TUZATISH: Docker clientini o'rnatish
# Bu Go ilovasiga "docker" buyrug'ini chaqirish imkonini beradi
RUN apk add --no-cache docker-cli

# Konteyner ichida ish katalogini belgilash
WORKDIR /app

# Build bosqichidan kompilyatsiya qilingan binaryni nusxalash
COPY --from=builder /app/code-executor .

# Server ishga tushganda bajariladigan buyruq
CMD ["./code-executor"]

# Server qaysi portda tinglashini bildirish (ixtiyoriy, faqat hujjatlashtirish uchun)
EXPOSE 8080
