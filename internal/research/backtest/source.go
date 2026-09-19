package backtest

import "github.com/bibhuyash/tradeedge/internal/research/model"

type DatasetSource struct{ dataset model.Dataset }

func NewDatasetSource(dataset model.Dataset) DatasetSource      { return DatasetSource{dataset: dataset} }
func (s DatasetSource) DatasetVersion() string                  { return s.dataset.Version() }
func (s DatasetSource) Instruments() []model.ResearchInstrument { return s.dataset.Instruments() }
func (s DatasetSource) Observations() []model.Observation       { return s.dataset.Observations() }
