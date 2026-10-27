FROM golang:1.27.3-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /out/api ./cmd/api && go build -o /out/worker ./cmd/worker && go build -o /out/orchestrator ./cmd/orchestrator && go build -o /out/connector ./cmd/connector

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata docker-cli
COPY --from=build /out/api /api
COPY --from=build /out/worker /worker
COPY --from=build /out/orchestrator /orchestrator
COPY --from=build /out/connector /connector
ENTRYPOINT ["/api"]
