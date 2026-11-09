# Imagen de producción de la API Sphere (binario estático, ~15 MB)
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sphere-api .

FROM alpine:3.20
# Certificados para HTTPS (Transbank, Mailjet, Web Push, IAM) y zona horaria
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H sphere
COPY --from=build /out/sphere-api /usr/local/bin/sphere-api
USER sphere
ENV PORT=4001
EXPOSE 4001
ENTRYPOINT ["/usr/local/bin/sphere-api"]
