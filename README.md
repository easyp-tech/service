# EasyP Plugin Server

Сервис для обработки запросов генерации кода плагинов, использующий `pluginpb.CodeGeneratorRequest` и информацию о плагине.

**Модуль:** `github.com/easyp-tech/easyp-plugin-server`

## Структура проекта

```
.
├── api/
│   └── plugin-generator/
│       └── v1/
│           ├── plugin_service.proto        # API контракт
│           ├── plugin_service.pb.go        # Сгенерированные Go типы
│           └── plugin_service_grpc.pb.go   # Сгенерированный gRPC код
├── cmd/
│   └── server/
│       └── main.go                    # Точка входа сервера
├── internal/
│   ├── clients/
│   │   └── docker_plugin_runner/      # Docker plugin runner клиент
│   │       ├── interface.go
│   │       └── runner.go
│   ├── config/
│   │   └── config.go                  # Конфигурация сервиса
│   ├── service/
│   │   └── plugin_service.go          # Сервисный слой
│   └── transport/
│       └── grpc_server.go             # Транспортный слой (gRPC)
├── easyp.yaml                         # Конфигурация easyp
├── Makefile                          # Автоматизация сборки
├── go.mod                            # Go модуль
└── README.md                         # Документация
```

## Установка зависимостей

### Предварительные требования
Убедитесь, что у вас установлен Protocol Buffer Compiler (protoc):

```bash
# macOS
brew install protobuf

# Ubuntu/Debian
sudo apt-get install protobuf-compiler

# или скачайте с https://github.com/protocolbuffers/protobuf/releases
```

### Установка проектных зависимостей

1. Установите easyp и protoc плагины:
```bash
make install-deps
```

2. Загрузите зависимости модулей:
```bash
make mod-download
```

## Генерация Go кода

Для генерации Go кода из proto файлов выполните:

```bash
make generate
```

Эта команда:
- Загрузит зависимости модулей
- Сгенерирует Go код из proto файлов в директории `api/`

**Важно:** Сгенерированные protobuf файлы (`*.pb.go`, `*_grpc.pb.go`) включены в репозиторий для использования клиентами. При изменении `.proto` файлов не забудьте запустить `make generate` и закоммитить обновленные файлы.

## Доступные команды

- `make generate` - Генерация Go кода из proto файлов
- `make lint` - Проверка proto файлов линтером
- `make clean` - Удаление сгенерированных файлов
- `make mod-download` - Загрузка зависимостей модулей
- `make mod-update` - Обновление зависимостей модулей
- `make mod-vendor` - Создание vendor директории
- `make breaking` - Проверка breaking changes
- `make rebuild` - Полная пересборка (clean + generate)

## API контракт

Сервис `PluginGeneratorService` предоставляет метод `GenerateCode`, который принимает:

1. `google.protobuf.compiler.CodeGeneratorRequest` - стандартный запрос генератора кода
2. `plugin_info` - строку с именем и версией плагина в формате "name:version" (например, "python:v32.1")

Ответ содержит:
- `google.protobuf.compiler.CodeGeneratorResponse` - результат генерации кода
- `status` - статус обработки
- `message` - дополнительные сообщения или ошибки

## Запуск сервера

Для запуска сервера выполните:

```bash
# Сборка сервера
go build -o bin/server ./cmd/server

# Запуск с настройками по умолчанию
./bin/server

# Или с кастомными настройками через переменные окружения
SERVER_HOST=0.0.0.0 SERVER_PORT=9090 REGISTRY_URL=my-registry.com ./bin/server
```

## Конфигурация

Сервис настраивается через переменные окружения:

- `SERVER_HOST` - хост для запуска сервера (по умолчанию: localhost)
- `SERVER_PORT` - порт для запуска сервера (по умолчанию: 8080)
- `REGISTRY_URL` - URL реестра Docker образов (по умолчанию: yakwilik)

## Линтинг

Проект настроен с линтером easyp для проверки качества proto файлов:

```bash
# Проверка proto файлов линтером
make lint
```

Линтер использует стандартные правила buf с дополнительными настройками:
- Исключение правила `PACKAGE_DIRECTORY_MATCH` для гибкости структуры
- Суффикс `_UNSPECIFIED` для нулевых значений enum
- Разрешение использования `google.protobuf.Empty` в запросах и ответах
- Требование суффикса `Service` для сервисов

## Конфигурация easyp

Файл `easyp.yaml` настроен для:
- **Линтинга:** проверка качества proto файлов с настраиваемыми правилами
- **Генерации:** Go код с относительными путями (`paths: source_relative`)
- **gRPC:** поддержка с отключенным требованием нереализованных серверов
- **Зависимости:** использование googleapis для стандартных типов protobuf

Сгенерированный код размещается рядом с `.proto` файлами в соответствии с `go_package` опцией.

## Использование клиентами

Для использования сервиса в Go проектах добавьте зависимость:

```bash
go get github.com/easyp-tech/easyp-plugin-server
```

Пример использования:

```go
import (
    plugingeneratorv1 "github.com/easyp-tech/easyp-plugin-server/api/plugin-generator/v1"
    "google.golang.org/grpc"
)

// Подключение к сервису
conn, err := grpc.Dial("localhost:8080", grpc.WithInsecure())
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

client := plugingeneratorv1.NewPluginGeneratorServiceClient(conn)

// Использование
response, err := client.GenerateCode(ctx, &plugingeneratorv1.GenerateCodeRequest{
    CodeGeneratorRequest: codeGenRequest,
    PluginInfo:           "python:v32.1",
})
```
