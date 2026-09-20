package dataset

import (
	"errors"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"sort"
	"time"
)

type BarInterval time.Duration

const (
	Bar1Minute  BarInterval = BarInterval(time.Minute)
	Bar5Minute  BarInterval = BarInterval(5 * time.Minute)
	Bar15Minute BarInterval = BarInterval(15 * time.Minute)
)

type Bar struct {
	InstrumentID           string
	Open, High, Low, Close int64
	Volume                 *int64
	OpenInterest           *int64
	StartTime, EndTime     time.Time
}

func AggregateBars(observations []Observation, interval BarInterval) ([]Bar, error) {
	d := time.Duration(interval)
	if d != time.Minute && d != 5*time.Minute && d != 15*time.Minute {
		return nil, ErrInvalid
	}
	groups := map[string]*Bar{}
	volumes := map[string]int64{}
	volumeKnown := map[string]bool{}
	for _, o := range sortedObservations(observations) {
		start := o.ExchangeTimestamp.UTC().Truncate(d)
		key := o.InstrumentID + "|" + start.Format(time.RFC3339Nano)
		b := groups[key]
		if b == nil {
			b = &Bar{InstrumentID: o.InstrumentID, Open: o.LTPMinor, High: o.LTPMinor, Low: o.LTPMinor, Close: o.LTPMinor, StartTime: start, EndTime: start.Add(d)}
			groups[key] = b
		} else {
			if o.LTPMinor > b.High {
				b.High = o.LTPMinor
			}
			if o.LTPMinor < b.Low {
				b.Low = o.LTPMinor
			}
			b.Close = o.LTPMinor
		}
		if o.Volume != nil {
			volumes[key] += *o.Volume
			volumeKnown[key] = true
		}
		if o.OpenInterest != nil {
			v := *o.OpenInterest
			b.OpenInterest = &v
		}
	}
	out := make([]Bar, 0, len(groups))
	for k, b := range groups {
		if volumeKnown[k] {
			v := volumes[k]
			b.Volume = &v
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndTime.Before(out[j].EndTime) })
	return out, nil
}
func AvailableBars(bars []Bar, at time.Time) []Bar {
	out := []Bar{}
	for _, b := range bars {
		if !at.Before(b.EndTime) {
			out = append(out, b)
		}
	}
	return out
}

type OptionChainEntry struct {
	Instrument          Instrument
	Observation         Observation
	StrikeDistanceMinor int64
	Moneyness           string
}
type OptionChainSnapshot struct {
	Underlying     string
	Timestamp      time.Time
	Expiry         string
	ATMStrikeMinor int64
	Entries        []OptionChainEntry
}

func NewOptionChainSnapshot(d CanonicalDataset, underlying string, at time.Time, expiry string, referenceMinor int64) (OptionChainSnapshot, error) {
	if referenceMinor < 0 {
		return OptionChainSnapshot{}, ErrInvalid
	}
	latest := map[string]Observation{}
	meta := map[string]Instrument{}
	for _, i := range d.Instruments {
		if i.Underlying == underlying && i.Type == domain.InstrumentOption && i.Expiry == expiry && i.AvailableAt(at) {
			meta[i.ID] = i
		}
	}
	for _, o := range d.Observations {
		if _, ok := meta[o.InstrumentID]; ok && !o.ExchangeTimestamp.After(at) {
			if prev, exists := latest[o.InstrumentID]; !exists || prev.ExchangeTimestamp.Before(o.ExchangeTimestamp) {
				latest[o.InstrumentID] = o
			}
		}
	}
	if len(latest) == 0 {
		return OptionChainSnapshot{}, errors.New("no point-in-time option observations")
	}
	strikes := []int64{}
	seen := map[int64]bool{}
	for id := range latest {
		s := *meta[id].StrikeMinor
		if !seen[s] {
			seen[s] = true
			strikes = append(strikes, s)
		}
	}
	sort.Slice(strikes, func(i, j int) bool { return strikes[i] < strikes[j] })
	atm := strikes[0]
	for _, s := range strikes {
		if abs(s-referenceMinor) < abs(atm-referenceMinor) || (abs(s-referenceMinor) == abs(atm-referenceMinor) && s < atm) {
			atm = s
		}
	}
	entries := []OptionChainEntry{}
	for id, o := range latest {
		i := meta[id]
		dist := *i.StrikeMinor - referenceMinor
		m := "ATM"
		if dist < 0 {
			if i.OptionType == "CE" {
				m = "ITM"
			} else {
				m = "OTM"
			}
		} else if dist > 0 {
			if i.OptionType == "CE" {
				m = "OTM"
			} else {
				m = "ITM"
			}
		}
		entries = append(entries, OptionChainEntry{i, o, dist, m})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].Instrument, entries[j].Instrument
		if *a.StrikeMinor != *b.StrikeMinor {
			return *a.StrikeMinor < *b.StrikeMinor
		}
		return a.OptionType < b.OptionType
	})
	return OptionChainSnapshot{underlying, at.UTC(), expiry, atm, entries}, nil
}
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

type Rollover struct {
	At               time.Time `json:"at"`
	FromInstrumentID string    `json:"from_instrument_id"`
	ToInstrumentID   string    `json:"to_instrument_id"`
}
type ContinuousFuturePolicy struct {
	Version             string     `json:"version"`
	Underlying          string     `json:"underlying"`
	InitialInstrumentID string     `json:"initial_instrument_id"`
	Rollovers           []Rollover `json:"rollovers"`
}

func (p ContinuousFuturePolicy) ContractAt(at time.Time) (string, error) {
	if p.Version == "" || p.InitialInstrumentID == "" {
		return "", errors.New("continuous futures rollover policy required")
	}
	current := p.InitialInstrumentID
	rolls := append([]Rollover(nil), p.Rollovers...)
	sort.Slice(rolls, func(i, j int) bool { return rolls[i].At.Before(rolls[j].At) })
	for _, r := range rolls {
		if at.Before(r.At) {
			break
		}
		if current != r.FromInstrumentID {
			return "", ErrInvalid
		}
		current = r.ToInstrumentID
	}
	return current, nil
}
