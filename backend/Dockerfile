FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/inventory-api ./cmd/server

FROM alpine:3.21
RUN addgroup -S app && adduser -S app -G app && apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/inventory-api /app/inventory-api
USER app
EXPOSE 8080
ENTRYPOINT ["/app/inventory-api"]
