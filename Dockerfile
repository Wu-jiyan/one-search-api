# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/onesearch ./cmd/onesearch

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=build /out/onesearch /app/onesearch
EXPOSE 8080
USER app
ENTRYPOINT ["/app/onesearch"]
