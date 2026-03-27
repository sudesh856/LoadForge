package dashboard

import (
	"embed"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed dashboard.html
var DashboardHTML embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type MetricsSnapshot struct {
	Timestamp int64          `json:"timestamp"`
	RPS       float64        `json:"rps"`
	P50       int64          `json:"p50"`
	P95       int64          `json:"p95"`
	P99       int64          `json:"p99"`
	ErrorRate float64        `json:"error_rate"`
	TotalReqs int64          `json:"total_requests"`
	ActiveVUs int            `json:"active_vus"`
	Endpoints []EndpointStat `json:"endpoints,omitempty"`
}

type EndpointStat struct {
	Name      string  `json:"name"`
	Requests  int64   `json:"requests"`
	RPS       float64 `json:"rps"`
	P99       int64   `json:"p99"`
	ErrorRate float64 `json:"error_rate"`
}

// RunRecord lives here — store implements the Store interface below
type RunRecord struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Timestamp int64  `json:"timestamp"`
	Summary   string `json:"summary"`
}

// Store interface — store.Store must implement these two methods
type Store interface {
	ListRunsRaw() ([]RunRecord, error)
	GetRunRaw(id int64) (*RunRecord, error)
}

type Server struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
	cancel  func()
	store   Store
}

func New(cancel func(), store Store) *Server {
	return &Server{
		clients: make(map[*websocket.Conn]bool),
		cancel:  cancel,
		store:   store,
	}
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (s *Server) HandleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.cancel()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) HandleRuns(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.store == nil {
		w.Write([]byte("[]"))
		return
	}
	runs, err := s.store.ListRunsRaw()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []RunRecord{}
	}
	json.NewEncoder(w).Encode(runs)
}

func (s *Server) HandleCompare(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "store not available", http.StatusServiceUnavailable)
		return
	}
	id1, err1 := strconv.ParseInt(r.URL.Query().Get("a"), 10, 64)
	id2, err2 := strconv.ParseInt(r.URL.Query().Get("b"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid ids: use ?a=1&b=2", http.StatusBadRequest)
		return
	}
	r1, e1 := s.store.GetRunRaw(id1)
	r2, e2 := s.store.GetRunRaw(id2)
	if e1 != nil || e2 != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"run_a": r1,
		"run_b": r2,
	})
}

func (s *Server) Broadcast(snap MetricsSnapshot) {
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.clients {
		conn.WriteMessage(websocket.TextMessage, data)
	}
}

func (s *Server) Start(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.HandleWS)
	mux.HandleFunc("/api/stop", s.HandleStop)
	mux.HandleFunc("/api/runs", s.HandleRuns)
	mux.HandleFunc("/api/compare", s.HandleCompare)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := DashboardHTML.ReadFile("dashboard.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	go srv.ListenAndServe()
}

func (s *Server) StartBroadcasting(getSnapshot func() MetricsSnapshot, ctx interface{ Done() <-chan struct{} }) {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Broadcast(getSnapshot())
			}
		}
	}()
}