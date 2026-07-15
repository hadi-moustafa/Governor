// Command mockprovider runs a fake streaming LLM backend: an SSE server
// that emits chunks with fake token counts, zero API cost. Used for all
// dev/testing of the gateway without hitting a real provider.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/hadi-moustafa/governor/internal/mockprovider"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	chunks := flag.Int("chunks", 40, "fake chunks to stream per request")
	tokensPerChunk := flag.Int("tokens-per-chunk", 8, "fake tokens per chunk")
	delay := flag.Duration("delay", 200*time.Millisecond, "delay between chunks")
	flag.Parse()

	srv := mockprovider.NewServer(mockprovider.Config{
		Chunks:         *chunks,
		TokensPerChunk: *tokensPerChunk,
		ChunkDelay:     *delay,
	})

	log.Printf("mockprovider listening on %s (%d chunks, %v apart, %d tokens/chunk)", *addr, *chunks, *delay, *tokensPerChunk)
	log.Fatal(http.ListenAndServe(*addr, srv))
}
