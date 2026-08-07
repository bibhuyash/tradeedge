package model

import "github.com/bibhuyash/tradeedge/internal/domain"

// CostBasis is authoritative integer money. The alias preserves the shared
// currency and checked-arithmetic boundary used by portfolio risk.
type CostBasis = domain.Money

// RealizedPnL is signed authoritative integer money.
type RealizedPnL = domain.Money

type NetQuantity int64
type ClosedQuantity int64
type OpenQuantity int64

func (value NetQuantity) Int64() int64     { return int64(value) }
func (value ClosedQuantity) Int64() int64  { return int64(value) }
func (value OpenQuantity) Int64() int64    { return int64(value) }
func (value ClosedQuantity) IsValid() bool { return value >= 0 }
func (value OpenQuantity) IsValid() bool   { return value >= 0 }
