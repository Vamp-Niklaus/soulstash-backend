package traffic

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"soulstash/internal/domain"
	"soulstash/internal/platform/mongodb"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Buffer provides an in-memory batching buffer for traffic telemetry
type Buffer struct {
	mu     sync.Mutex
	buffer []domain.TrafficLog
	db     *mongo.Database
	logger *slog.Logger
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewBuffer instantiates a new thread-safe traffic buffer
func NewBuffer(db *mongo.Database, logger *slog.Logger) *Buffer {
	b := &Buffer{
		buffer: make([]domain.TrafficLog, 0, 100),
		db:     db,
		logger: logger,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}

	go b.startFlushWorker()
	return b
}

// Push appends a traffic log to the buffer
func (b *Buffer) Push(log domain.TrafficLog) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Apply local IP geo enrichment
	enrichGeo(&log)
	b.buffer = append(b.buffer, log)
}

func (b *Buffer) startFlushWorker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer close(b.doneCh)

	for {
		select {
		case <-ticker.C:
			b.Flush()
		case <-b.stopCh:
			b.Flush()
			return
		}
	}
}

// Flush writes all accumulated records to MongoDB in a single batch
func (b *Buffer) Flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.buffer
	b.buffer = make([]domain.TrafficLog, 0, 100)
	b.mu.Unlock()

	if b.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	coll := b.db.Collection(mongodb.CollectionTrafficLogs)
	docs := make([]any, len(batch))
	for i, item := range batch {
		docs[i] = item
	}

	_, err := coll.InsertMany(ctx, docs)
	if err != nil {
		b.logger.Error("Failed to flush traffic logs to MongoDB", slog.String("error", err.Error()))
		// Prepend back to buffer so records are not dropped
		b.mu.Lock()
		b.buffer = append(batch, b.buffer...)
		b.mu.Unlock()
		return
	}

	b.logger.Info("[Traffic] Inserted records into TrafficLogsMonolith", slog.Int("count", len(docs)))
}

// Close gracefully flushes the buffer and waits for worker to finish
func (b *Buffer) Close() {
	close(b.stopCh)
	<-b.doneCh
}

func enrichGeo(log *domain.TrafficLog) {
	ip := log.IP
	if ip == "127.0.0.1" || ip == "::1" || strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") {
		log.Country = "Local"
		log.City = "Localhost"
	} else if log.Country == "" {
		log.Country = "Unknown"
		log.City = "Unknown"
	}
}
