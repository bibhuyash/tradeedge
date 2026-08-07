package testfixture

import (
	"fmt"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var BaseTime = time.Date(2026, time.January, 5, 9, 15, 0, 0, time.UTC)

func Fill(sequence int, side domain.Side, quantity, price int64, occurredAt, receivedAt time.Time) (accountingmodel.AccountingFill, error) {
	return FillWithIdentity(fmt.Sprintf("execution-%03d", sequence), side, quantity, price, occurredAt, receivedAt)
}

func FillWithIdentity(executionID string, side domain.Side, quantity, price int64, occurredAt, receivedAt time.Time) (accountingmodel.AccountingFill, error) {
	fixture, err := executionfixture.New(false)
	if err != nil {
		return accountingmodel.AccountingFill{}, err
	}
	order := fixture.Orders[0]
	report, err := executionmodel.NewExecutionReport(executionmodel.ExecutionReportSpec{SchemaVersion: "execution-report/v1", Source: "accounting-fixture", SourceEventID: "report-" + executionID, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), Type: executionmodel.ReportFill, Reason: executionmodel.ReasonBrokerFill, CumulativeFilled: quantity, OccurredAt: occurredAt, ReceivedAt: receivedAt})
	if err != nil {
		return accountingmodel.AccountingFill{}, err
	}
	fillQuantity, err := domain.NewQuantity(quantity)
	if err != nil {
		return accountingmodel.AccountingFill{}, err
	}
	fillPrice, err := domain.NewPrice(price, "INR")
	if err != nil {
		return accountingmodel.AccountingFill{}, err
	}
	fill, err := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "accounting-fixture", SourceExecutionID: executionID, OrderID: order.ID(), ReportID: report.ID(), Quantity: fillQuantity, Price: fillPrice, OccurredAt: occurredAt})
	if err != nil {
		return accountingmodel.AccountingFill{}, err
	}
	portfolioID, _ := portfoliomodel.NewPortfolioID("accounting-fixture")
	instrumentID, _ := domain.InstrumentIDFromCanonicalKey("accounting-instrument")
	return accountingmodel.NewAccountingFill(accountingmodel.AccountingFillSpec{SchemaVersion: "accounting-fill/v1", Fill: fill, PortfolioID: portfolioID, InstrumentID: instrumentID, Side: side, ReceivedAt: receivedAt})
}
