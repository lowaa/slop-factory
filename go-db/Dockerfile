FROM golang:1.27-bookworm

WORKDIR /srv

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o demo-api ./cmd/demo-api

CMD ["/srv/demo-api"]
