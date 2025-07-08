FROM golang:1.22-alpine as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# compile app
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /my-voice-app ./src/main.go


FROM gcr.io/distroless/static-debian11

COPY --from=builder /my-voice-app /my-voice-app

COPY ./config.yml ./config.yml

EXPOSE 8000
EXPOSE 8080

CMD ["/my-voice-app"]