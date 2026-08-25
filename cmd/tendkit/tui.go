package main

import (
	"context"
	"errors"
	"os"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/service"
	"github.com/eoctet/tendkit/internal/ui"
	"github.com/eoctet/tendkit/pkg/i18n"
)

var executeTUI = ui.RunTUI

func runInteractiveTUI(ctx context.Context, applicationService *service.Service, color ui.Mode) error {
	actions := ui.TUIActions{
		OperationLog:        applicationService.OperationLog,
		OperationText:       applicationService.OperationText,
		CommandOutputWriter: applicationService.CommandOutputWriter,
		Load: func() (model.Config, model.RuntimeState, error) {
			return applicationService.Load()
		},
		Reload: func() (model.Config, model.RuntimeState, error) {
			return applicationService.Reload()
		},
		SaveConfig: func(expected, proposed model.Config) (model.Config, error) {
			return applicationService.SaveConfig(expected, proposed)
		},
		Scan: func(scanContext context.Context, request ui.TUIScanRequest, observer ui.TUIScanObserver) (ui.TUIScanSnapshot, error) {
			scanObserver := service.ScanObserver{Progress: func(progress model.ScanProgress) {
				if observer.Progress != nil {
					observer.Progress(progress.Stage, progress.Subject)
				}
			}}
			var preview service.ScanPreview
			var err error
			if request.Application == nil {
				preview, err = applicationService.PreviewScan(scanContext, scanObserver)
			} else {
				preview, err = applicationService.PreviewApplicationScan(scanContext, *request.Application, scanObserver)
			}
			return ui.TUIScanSnapshot{
				BaseConfig: preview.BaseConfig, BaseState: preview.BaseState,
				Config: preview.Config, State: preview.State, Changes: preview.Changes,
				Added: preview.Added, Removed: preview.Removed, Excluded: preview.Excluded,
			}, err
		},
		SaveScan: func(expectedCatalog, catalog model.Config) (model.Config, error) {
			if err := applicationService.SaveScanSnapshot(expectedCatalog, catalog); err != nil {
				return model.Config{}, err
			}
			saved, _, err := applicationService.Load()
			return saved, err
		},
		GenerateIdentity: func(application model.Application) (string, error) {
			identity, err := applicationService.GenerateIdentity(ctx, application)
			if err != nil {
				return "", err
			}
			if identity == "" {
				return "", errors.New(i18n.T("tui.scan.identity_unavailable"))
			}
			return identity, nil
		},
		DownloadAssetCandidates: func(runContext context.Context, request ui.TUIRunRequest, observer ui.TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error) {
			return applicationService.DownloadAssetCandidates(runContext, request.Names, observer.Progress)
		},
		StartRun: func(runContext context.Context, request ui.TUIRunRequest, observer ui.TUIObserver) (*ui.TUIRunBatch, error) {
			options := service.RunOptions{
				Names: request.Names, CheckOnly: request.CheckOnly, DownloadAssets: request.DownloadAssets,
				DownloadOutput: observer.DownloadOutput,
				CommandOutput:  observer.CommandOutput,
				Observer: service.RunObserver{
					AppStart: observer.AppStart, Result: observer.Result,
					UpdateStart:   observer.UpdateStart,
					DownloadStart: observer.DownloadStart, DownloadProgress: observer.DownloadProgress,
				},
			}
			batch := service.NewBatch(options)
			type outcome struct {
				config  model.Config
				results []model.Result
				err     error
			}
			finished := make(chan outcome, 1)
			go func() {
				config, results, err := applicationService.RunBatch(runContext, options, batch)
				finished <- outcome{config: config, results: results, err: err}
			}()
			return &ui.TUIRunBatch{
				AddRequest: func(addition ui.TUIRunRequest) error {
					return batch.Add(service.RunOptions{Names: addition.Names, CheckOnly: addition.CheckOnly, DownloadAssets: addition.DownloadAssets})
				},
				WaitResult: func() (model.Config, []model.Result, error) {
					result := <-finished
					return result.config, result.results, result.err
				},
			}, nil
		},
	}
	err := executeTUI(ctx, os.Stdin, os.Stdout, actions, color)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
