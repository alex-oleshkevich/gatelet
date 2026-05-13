FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateletd ./cmd/gateletd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gatelet ./cmd/gatelet

FROM alpine:3.20

RUN adduser -D -H -u 10001 gatelet
COPY --from=build /out/gateletd /usr/local/bin/gateletd
COPY --from=build /out/gatelet /usr/local/bin/gatelet

USER gatelet
EXPOSE 8080 4443

ENTRYPOINT ["gateletd"]
