package ratelimiter

import (
	"context"
	"net"

	"google.golang.org/grpc/peer"

	"github.com/easyp-tech/service/internal/core"
)

// KeyExtractor определяет стратегию извлечения ключа rate limiting из контекста.
// Возвращает ключ (например, client IP) или пустую строку, если ключ не найден.
// При пустом ключе rate limiting пропускается (fail-open).
type KeyExtractor func(ctx context.Context) string

// PeerIPExtractor извлекает адрес вызывающего из контекста.
//
// Сначала берётся значение, которое положил интерсептор callerIP: он работает в
// базовой цепочке, то есть раньше лимитеров, и уже учёл заголовки доверенного
// прокси. Раньше здесь читался напрямую peer.FromContext — при том, что
// комментарий утверждал обратное. За ingress это означало, что весь трафик
// приходит с одного адреса и складывается в одно ведро: лимит становился общим
// на всех вызывающих сразу.
//
// Откат на peer оставлен для вызовов в обход интерсептора — например в тестах.
func PeerIPExtractor(ctx context.Context) string {
	// "unknown" — это признак того, что адрес определить не удалось, а не адрес.
	// Считать его ключом значило бы свести весь безадресный трафик в одно ведро,
	// то есть повторить исходный дефект под другим именем.
	if ip := core.CallerIPFromContext(ctx); ip != "" && ip != grpcCallerIPUnknown {
		return ip
	}

	peerInfo, ok := peer.FromContext(ctx)
	if !ok || peerInfo.Addr == nil {
		return ""
	}

	host, _, err := net.SplitHostPort(peerInfo.Addr.String())
	if err != nil {
		return peerInfo.Addr.String()
	}

	return host
}

// grpcCallerIPUnknown дублирует grpchelper.CallerIPUnknown. Импортировать
// grpchelper отсюда нельзя: он сам зависит от core, а лимитеры подключаются в
// его цепочку — получилось бы кольцо. Значение проверяется тестом на обеих
// сторонах.
const grpcCallerIPUnknown = "unknown"
