package shadowruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/qualification"
)

func (r *Runtime) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked()
}

func (r *Runtime) snapshotLocked() Snapshot {
	ema := []EMAState{r.ema[qualification.NIFTY], r.ema[qualification.BANKNIFTY]}
	status := []UnderlyingStatus{r.status[qualification.NIFTY], r.status[qualification.BANKNIFTY]}
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion,
		Revision:      r.revision,
		TradingDate:   r.tradingDate,
		Candles:       r.aggregator.Snapshot(),
		EMA:           ema,
		Qualification: r.qualification.Snapshot(),
		Sessions:      cloneSessionTracker(*r.sessions),
		Status:        status,
		LastRemapAt:   cloneTimeMap(r.lastRemap),
	}
	snapshot.Checksum = snapshotChecksum(snapshot)
	return snapshot
}

func (r *Runtime) Restore(snapshot Snapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if snapshot.SchemaVersion != SchemaVersion || snapshot.Revision == 0 ||
		snapshot.TradingDate != r.tradingDate || snapshot.Checksum != snapshotChecksum(snapshot) ||
		len(snapshot.EMA) != 2 || len(snapshot.Status) != 2 {
		return ErrInvalid
	}
	if err := r.aggregator.Restore(snapshot.Candles); err != nil {
		return err
	}
	if err := r.qualification.Restore(snapshot.Qualification); err != nil {
		return err
	}
	ema := map[qualification.Underlying]EMAState{}
	for _, state := range snapshot.EMA {
		if (state.Underlying != qualification.NIFTY && state.Underlying != qualification.BANKNIFTY) || ema[state.Underlying].Underlying != "" {
			return ErrInvalid
		}
		ema[state.Underlying] = state
	}
	status := map[qualification.Underlying]UnderlyingStatus{}
	for _, state := range snapshot.Status {
		if (state.Underlying != qualification.NIFTY && state.Underlying != qualification.BANKNIFTY) || status[state.Underlying].Underlying != "" {
			return ErrInvalid
		}
		status[state.Underlying] = state
	}
	if len(snapshot.Sessions.Current) != 2 || len(snapshot.Sessions.Opening) != 2 {
		return ErrInvalid
	}
	r.ema = ema
	r.status = status
	r.sessions = &snapshot.Sessions
	r.sessions.Restart()
	r.lastRemap = cloneTimeMap(snapshot.LastRemapAt)
	r.revision = snapshot.Revision
	return nil
}

func (r *Runtime) CloseSession(at time.Time, complete bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if at.IsZero() {
		return ErrInvalid
	}
	for _, underlying := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		card := r.sessions.Current[underlying]
		card.DataGaps += r.aggregator.series[underlying].GapMinutes
		r.sessions.Current[underlying] = card
		if r.aggregator.series[underlying].GapMinutes > 0 {
			r.sessions.AddReason(underlying, ReasonMarketDataGap)
		}
	}
	if err := r.sessions.Close(at, r.qualification.Scorecards(), r.qualification.Snapshot(), complete); err != nil {
		return err
	}
	r.revision++
	for _, card := range r.sessions.Closed[len(r.sessions.Closed)-2:] {
		r.emit(notificationSpec{
			source: card.SessionID, at: at, kind: "closed", underlying: card.Underlying,
			subject: string(card.Quality) + "; checksum=" + card.Checksum,
		})
	}
	return nil
}

func snapshotChecksum(value Snapshot) string {
	value.Checksum = ""
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneTimeMap(value map[qualification.Underlying]time.Time) map[qualification.Underlying]time.Time {
	result := make(map[qualification.Underlying]time.Time, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func cloneSessionTracker(value SessionTracker) SessionTracker {
	raw, _ := json.Marshal(value)
	var result SessionTracker
	_ = json.Unmarshal(raw, &result)
	sort.Slice(result.Closed, func(i, j int) bool {
		return result.Closed[i].SessionID < result.Closed[j].SessionID
	})
	return result
}
