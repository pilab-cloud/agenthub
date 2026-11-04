# syntax=docker/dockerfile:1
FROM oven/bun:1 AS web
WORKDIR /src
COPY internal/web/package.json internal/web/bun.lock* internal/web/
RUN cd internal/web && bun install --frozen-lockfile || cd internal/web && bun install
COPY internal/web ./internal/web
RUN cd internal/web && bun run build:css

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/web/static/app.css ./internal/web/static/app.css
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/agenthub-server ./cmd/agenthub-server \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/agenthub ./cmd/agenthub

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/agenthub-server /agenthub-server
COPY --from=build /out/agenthub /agenthub
EXPOSE 8080 9100
ENTRYPOINT ["/agenthub-server"]
CMD ["serve", "--config", "/etc/agenthub/config.yaml"]
