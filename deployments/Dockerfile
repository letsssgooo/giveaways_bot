ARG GO_VERSION=1.26.5
FROM golang:${GO_VERSION} AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/giveawaysbot ./cmd/giveawaysbot

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/giveawaysbot /giveawaysbot

ENTRYPOINT ["/giveawaysbot"]
