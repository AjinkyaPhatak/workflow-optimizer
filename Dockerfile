FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY . .
RUN go build -o /out/api ./cmd/api

FROM alpine:3.20
COPY --from=build /out/api /api
ENTRYPOINT ["/api"]
