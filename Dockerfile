# Сборка: один статический бинарник (шаблоны, статика и миграции встроены через go:embed).
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# timetzdata — база часовых поясов внутри бинарника (Asia/Almaty без системных файлов)
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata -ldflags="-s -w" -o /out/tropa ./cmd/tropa

# Запуск: минимальный образ без shell, от непривилегированного пользователя.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tropa /tropa
ENV ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/tropa"]
CMD ["serve"]
