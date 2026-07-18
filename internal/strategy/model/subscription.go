package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

const (
	MaximumSubscriptions = 32
	MaximumLookback      = 512
)

var ErrInvalidSubscription = errors.New("invalid strategy subscription")

type SubscriptionMode string

const (
	SubscriptionSingleStream    SubscriptionMode = "SINGLE_STREAM"
	SubscriptionExactCloseFrame SubscriptionMode = "EXACT_CLOSE_FRAME"
	SubscriptionLatestCompleted SubscriptionMode = "LATEST_COMPLETED_FRAME"
)

type InputSubscription struct {
	Role         string
	InstrumentID domain.InstrumentID
	Interval     marketmodel.CandleInterval
	Required     bool
	Trigger      bool
	Lookback     int
	MaximumAge   time.Duration
}

func (subscription InputSubscription) Validate() error {
	role := strings.TrimSpace(subscription.Role)
	if !definitionPattern.MatchString(role) || subscription.InstrumentID.IsZero() ||
		subscription.Lookback <= 0 || subscription.Lookback > MaximumLookback ||
		subscription.MaximumAge < 0 {
		return ErrInvalidSubscription
	}
	if _, valid := subscription.Interval.Duration(); !valid {
		return ErrInvalidSubscription
	}
	return nil
}

type SubscriptionSpec struct {
	mode          SubscriptionMode
	version       string
	subscriptions []InputSubscription
}

func NewSubscriptionSpec(
	mode SubscriptionMode,
	subscriptions []InputSubscription,
) (SubscriptionSpec, error) {
	if len(subscriptions) == 0 || len(subscriptions) > MaximumSubscriptions {
		return SubscriptionSpec{}, ErrInvalidSubscription
	}
	switch mode {
	case SubscriptionSingleStream, SubscriptionExactCloseFrame, SubscriptionLatestCompleted:
	default:
		return SubscriptionSpec{}, ErrInvalidSubscription
	}
	if mode == SubscriptionSingleStream && len(subscriptions) != 1 {
		return SubscriptionSpec{}, ErrInvalidSubscription
	}
	copied := append([]InputSubscription(nil), subscriptions...)
	keys := make([]string, len(copied))
	seen := make(map[string]struct{}, len(copied))
	required, triggers := 0, 0
	for index, subscription := range copied {
		if err := subscription.Validate(); err != nil {
			return SubscriptionSpec{}, err
		}
		key := fmt.Sprintf("%s|%s|%s", subscription.Role,
			subscription.InstrumentID, subscription.Interval)
		if _, exists := seen[key]; exists {
			return SubscriptionSpec{}, ErrInvalidSubscription
		}
		seen[key] = struct{}{}
		if subscription.Required {
			required++
		}
		if subscription.Trigger {
			triggers++
		}
		keys[index] = fmt.Sprintf("%s|%t|%t|%d|%d",
			key, subscription.Required, subscription.Trigger,
			subscription.Lookback, subscription.MaximumAge.Nanoseconds())
	}
	if required == 0 || triggers != 1 {
		return SubscriptionSpec{}, ErrInvalidSubscription
	}
	sort.Strings(keys)
	digest := sha256.Sum256([]byte("v1|" + string(mode) + "|" + strings.Join(keys, "\n")))
	sort.Slice(copied, func(i, j int) bool {
		left := copied[i].Role + "|" + copied[i].InstrumentID.String() + "|" + string(copied[i].Interval)
		right := copied[j].Role + "|" + copied[j].InstrumentID.String() + "|" + string(copied[j].Interval)
		return left < right
	})
	return SubscriptionSpec{
		mode: mode, version: hex.EncodeToString(digest[:]), subscriptions: copied,
	}, nil
}

func (spec SubscriptionSpec) IsZero() bool {
	return spec.version == "" || len(spec.subscriptions) == 0
}

func (spec SubscriptionSpec) Mode() SubscriptionMode { return spec.mode }
func (spec SubscriptionSpec) Version() string        { return spec.version }
func (spec SubscriptionSpec) Subscriptions() []InputSubscription {
	return append([]InputSubscription(nil), spec.subscriptions...)
}
