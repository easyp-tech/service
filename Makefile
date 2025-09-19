# Makefile для генерации Go кода из proto файлов с использованием easyptech/easyp

.PHONY: all build generate clean install-deps lint mod-download mod-update mod-vendor test test-bench

# Основная цель
all: build

# Сборка сервера
build: generate
	@echo "Собираем сервер..."
	go build -o bin/server ./cmd/server

# Установка зависимостей
install-deps:
	@echo "Устанавливаем easyp и protoc плагины..."
	go install github.com/easyp-tech/easyp/cmd/easyp@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Загрузка зависимостей модулей
mod-download:
	@echo "Загружаем зависимости модулей..."
	easyp -cfg easyp.yaml mod download

# Обновление зависимостей модулей
mod-update:
	@echo "Обновляем зависимости модулей..."
	easyp -cfg easyp.yaml mod update

# Создание vendor директории с зависимостями
mod-vendor:
	@echo "Создаем vendor директорию..."
	easyp -cfg easyp.yaml mod vendor

# Генерация Go кода из proto файлов
generate: mod-download
	@echo "Генерируем Go код из proto файлов..."
	easyp -cfg easyp.yaml generate

# Линтинг proto файлов
lint:
	@echo "Проверяем proto файлы линтером..."
	easyp -cfg easyp.yaml lint

# Очистка сгенерированных файлов
clean:
	@echo "Удаляем сгенерированные файлы..."
	find . -name "*.pb.go" -type f -delete
	find . -name "*_grpc.pb.go" -type f -delete
	rm -rf vendor/
	rm -rf bin/

# Инициализация проекта (если нужно)
init:
	@echo "Инициализируем easyp проект..."
	easyp init

# Проверка breaking changes (требует указания ветки)
breaking:
	@echo "Проверяем breaking changes против main ветки..."
	easyp breaking --against main

# Запуск тестов (требует запущенного сервера)
test:
	@echo "Запускаем тесты..."
	go test -v ./test

# Запуск бенчмарков (требует запущенного сервера)
test-bench:
	@echo "Запускаем бенчмарки..."
	go test -v -bench=. ./test

# Полная пересборка
rebuild: clean generate
