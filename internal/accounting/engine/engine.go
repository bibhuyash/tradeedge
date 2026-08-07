package engine

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidInput       = errors.New("invalid accounting engine input")
	ErrPositionMismatch   = errors.New("fill does not belong to position")
	ErrOutOfOrderFill     = errors.New("fill canonically precedes committed position state")
	ErrArithmeticOverflow = errors.New("accounting arithmetic overflow")
)

type Result struct {
	Snapshot    accountingmodel.PositionSnapshot
	Application accountingmodel.FillApplication
}

func Apply(current *accountingmodel.PositionSnapshot, fill accountingmodel.AccountingFill) (Result, error) {
	if fill.IsZero() {
		return Result{}, ErrInvalidInput
	}
	if current != nil {
		if current.IsZero() || current.ID() != fill.PositionID() {
			return Result{}, ErrPositionMismatch
		}
		if fill.OrderingKey().Compare(current.Spec().LastOrderingKey) <= 0 {
			return Result{}, ErrOutOfOrderFill
		}
	}
	fillSpec := fill.Spec().Fill.Spec()
	quantity, price := fillSpec.Quantity.Int64(), fillSpec.Price
	value, err := multiply(price.MinorUnits(), quantity)
	if err != nil {
		return Result{}, err
	}
	currency := price.Currency().String()
	zero, _ := domain.NewMoney(0, currency)

	var spec accountingmodel.PositionSnapshotSpec
	var previousRevision accountingmodel.PositionRevision
	var previousChecksum accountingmodel.StateChecksum
	if current == nil {
		spec = accountingmodel.PositionSnapshotSpec{
			SchemaVersion: "position-snapshot/v1", PositionID: fill.PositionID(), PortfolioID: fill.Spec().PortfolioID,
			InstrumentID: fill.Spec().InstrumentID, CumulativeBoughtValue: zero, CumulativeSoldValue: zero,
			GrossRealizedPnL: zero, AuthoritativeCharges: zero, OpenCostBasis: zero,
		}
	} else {
		spec = current.Spec()
		previousRevision, previousChecksum = current.Revision(), current.Checksum()
		if previousRevision == accountingmodel.PositionRevision(math.MaxUint64) {
			return Result{}, ErrArithmeticOverflow
		}
		if spec.OpenCostBasis.Currency() != price.Currency() {
			return Result{}, domain.ErrCurrencyMismatch
		}
	}

	oldNet, oldBasis := spec.NetQuantity.Int64(), spec.OpenCostBasis.MinorUnits()
	newNet, newBasis, closed, opened, allocated, realizedDelta, err := transition(oldNet, oldBasis, fill.Spec().Side, quantity, price.MinorUnits())
	if err != nil {
		return Result{}, err
	}
	if fill.Spec().Side == domain.SideBuy {
		spec.CumulativeBoughtQuantity, err = addNonnegative(spec.CumulativeBoughtQuantity, quantity)
		if err == nil {
			spec.CumulativeBoughtValue, err = addMoney(spec.CumulativeBoughtValue, value)
		}
	} else {
		spec.CumulativeSoldQuantity, err = addNonnegative(spec.CumulativeSoldQuantity, quantity)
		if err == nil {
			spec.CumulativeSoldValue, err = addMoney(spec.CumulativeSoldValue, value)
		}
	}
	if err != nil {
		return Result{}, err
	}
	spec.GrossRealizedPnL, err = addMoney(spec.GrossRealizedPnL, realizedDelta)
	if err != nil {
		return Result{}, err
	}
	spec.OpenCostBasis, _ = domain.NewMoney(newBasis, currency)
	spec.NetQuantity = accountingmodel.NetQuantity(newNet)
	spec.Revision = previousRevision + 1
	spec.LastOrderingKey = fill.OrderingKey()
	spec.LastFillID = fill.Spec().Fill.ID()
	if spec.AppliedFillCount == math.MaxUint64 {
		return Result{}, ErrArithmeticOverflow
	}
	spec.AppliedFillCount++
	spec.AppliedFillChecksum, err = appliedChecksum(spec.AppliedFillChecksum, fill)
	if err != nil {
		return Result{}, err
	}
	spec.UpdatedAt = fill.OrderingKey().OccurredAt

	if newNet == 0 {
		spec.OpenLot = nil
		spec.FlatAt = fill.OrderingKey().OccurredAt
	} else {
		newSide := domain.SideBuy
		if newNet < 0 {
			newSide = domain.SideSell
		}
		if oldNet == 0 || signsDiffer(oldNet, newNet) {
			spec.OpenedAt = fill.OrderingKey().OccurredAt
		}
		spec.FlatAt = zeroTime()
		basis, _ := domain.NewMoney(newBasis, currency)
		spec.OpenLot = &accountingmodel.PositionLot{Side: newSide, Quantity: accountingmodel.OpenQuantity(magnitude(newNet)), TotalBasis: basis,
			AverageNumerator: newBasis, AverageDenominator: magnitude(newNet), OpenedAt: spec.OpenedAt}
	}
	if spec.OpenedAt.IsZero() {
		spec.OpenedAt = fill.OrderingKey().OccurredAt
	}
	next, err := accountingmodel.NewPositionSnapshot(spec)
	if err != nil {
		return Result{}, err
	}
	applicationID, _ := accountingmodel.NewFillApplicationID(fill.PositionID(), fill.Spec().Fill.ID().String())
	allocatedMoney, _ := domain.NewMoney(allocated, currency)
	realizedMoney, _ := domain.NewMoney(realizedDelta, currency)
	application, err := accountingmodel.NewFillApplication(accountingmodel.FillApplicationSpec{
		SchemaVersion: "fill-application/v1", ID: applicationID, PositionID: fill.PositionID(), FillID: fill.Spec().Fill.ID(),
		FillChecksum: fill.Checksum(), OrderingKey: fill.OrderingKey(), PreviousRevision: previousRevision,
		PreviousSnapshotChecksum: previousChecksum, NextRevision: next.Revision(), NextSnapshotChecksum: next.Checksum(),
		ClosedQuantity: accountingmodel.ClosedQuantity(closed), OpenedQuantity: accountingmodel.OpenQuantity(opened), AllocatedClosingBasis: allocatedMoney,
		GrossRealizedDelta: realizedMoney, AppliedAt: fill.OrderingKey().ReceivedAt,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Snapshot: next, Application: application}, nil
}

func transition(net, basis int64, side domain.Side, quantity, price int64) (newNet, newBasis, closed, opened, allocated, realized int64, err error) {
	if quantity <= 0 || price <= 0 || (side != domain.SideBuy && side != domain.SideSell) || basis < 0 || net == math.MinInt64 {
		err = ErrInvalidInput
		return
	}
	delta := quantity
	if side == domain.SideSell {
		delta = -quantity
	}
	if net == 0 || (net > 0 && delta > 0) || (net < 0 && delta < 0) {
		newNet, err = checkedAdd(net, delta)
		if err != nil {
			return
		}
		value, multiplyErr := multiply(price, quantity)
		if multiplyErr != nil {
			err = multiplyErr
			return
		}
		newBasis, err = checkedAdd(basis, value)
		if err != nil {
			return
		}
		opened = quantity
		return
	}
	openQuantity := magnitude(net)
	closed = openQuantity
	if quantity < closed {
		closed = quantity
	}
	if closed == openQuantity {
		allocated = basis
	} else {
		allocated, err = proportionalBasis(basis, closed, openQuantity)
		if err != nil {
			return
		}
	}
	closingValue, multiplyErr := multiply(price, closed)
	if multiplyErr != nil {
		err = multiplyErr
		return
	}
	if net > 0 {
		realized, err = checkedSubtract(closingValue, allocated)
	} else {
		realized, err = checkedSubtract(allocated, closingValue)
	}
	if err != nil {
		return
	}
	newNet, err = checkedAdd(net, delta)
	if err != nil {
		return
	}
	if quantity < openQuantity {
		newBasis = basis - allocated
		return
	}
	if quantity == openQuantity {
		newBasis = 0
		return
	}
	opened = quantity - openQuantity
	newBasis, err = multiply(price, opened)
	return
}

func appliedChecksum(previous accountingmodel.StateChecksum, fill accountingmodel.AccountingFill) (accountingmodel.StateChecksum, error) {
	raw, err := json.Marshal(struct{ Previous, Fill, OccurredAt, ReceivedAt, FillID string }{
		previous.String(), fill.Checksum().String(), fill.OrderingKey().OccurredAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		fill.OrderingKey().ReceivedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), fill.Spec().Fill.ID().String()})
	if err != nil {
		return accountingmodel.StateChecksum{}, err
	}
	return accountingmodel.NewStateChecksum("accounting-applied-fill-chain/v1", raw)
}

func proportionalBasis(basis, closed, open int64) (int64, error) {
	product := new(big.Int).Mul(big.NewInt(basis), big.NewInt(closed))
	product.Quo(product, big.NewInt(open))
	if !product.IsInt64() {
		return 0, ErrArithmeticOverflow
	}
	return product.Int64(), nil
}
func multiply(left, right int64) (int64, error) {
	value := new(big.Int).Mul(big.NewInt(left), big.NewInt(right))
	if !value.IsInt64() {
		return 0, ErrArithmeticOverflow
	}
	return value.Int64(), nil
}
func checkedAdd(left, right int64) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) || (right < 0 && left < math.MinInt64-right) {
		return 0, ErrArithmeticOverflow
	}
	return left + right, nil
}
func checkedSubtract(left, right int64) (int64, error) {
	if right == math.MinInt64 {
		return 0, ErrArithmeticOverflow
	}
	return checkedAdd(left, -right)
}
func addNonnegative(left, right int64) (int64, error) {
	value, err := checkedAdd(left, right)
	if err != nil || value < 0 {
		return 0, ErrArithmeticOverflow
	}
	return value, nil
}
func addMoney(value domain.Money, delta int64) (domain.Money, error) {
	amount, _ := domain.NewMoney(delta, value.Currency().String())
	result, err := value.Add(amount)
	if err != nil {
		return domain.Money{}, ErrArithmeticOverflow
	}
	return result, nil
}
func magnitude(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
func signsDiffer(left, right int64) bool { return left != 0 && right != 0 && (left < 0) != (right < 0) }
func zeroTime() time.Time                { return time.Time{} }
