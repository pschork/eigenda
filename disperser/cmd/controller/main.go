package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Layr-Labs/eigenda/api/clients/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Layr-Labs/eigenda/common"
	"github.com/Layr-Labs/eigenda/common/aws/dynamodb"
	"github.com/Layr-Labs/eigenda/common/geth"
	"github.com/Layr-Labs/eigenda/common/healthcheck"
	"github.com/Layr-Labs/eigenda/core"
	"github.com/Layr-Labs/eigenda/core/eth"
	"github.com/Layr-Labs/eigenda/core/indexer"
	"github.com/Layr-Labs/eigenda/core/thegraph"
	corev2 "github.com/Layr-Labs/eigenda/core/v2"
	"github.com/Layr-Labs/eigenda/disperser/cmd/controller/flags"
	"github.com/Layr-Labs/eigenda/disperser/common/v2/blobstore"
	"github.com/Layr-Labs/eigenda/disperser/controller"
	"github.com/Layr-Labs/eigenda/disperser/encoder"
	gethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gammazero/workerpool"
	"github.com/urfave/cli"
)

var (
	version   string
	gitCommit string
	gitDate   string

	controllerMaxStallDuration = 240 * time.Second
)

func main() {
	app := cli.NewApp()
	app.Flags = flags.Flags
	app.Version = fmt.Sprintf("%s-%s-%s", version, gitCommit, gitDate)
	app.Name = "controller"
	app.Usage = "EigenDA Controller"
	app.Description = "EigenDA control plane for encoding and dispatching blobs"

	app.Action = RunController
	err := app.Run(os.Args)
	if err != nil {
		log.Fatalf("application failed: %v", err)
	}
	select {}
}

func RunController(ctx *cli.Context) error {
	config, err := NewConfig(ctx)
	if err != nil {
		return err
	}

	logger, err := common.NewLogger(&config.LoggerConfig)
	if err != nil {
		return err
	}

	// Reset readiness probe upon start-up
	if err := os.Remove(config.ControllerReadinessProbePath); err != nil {
		logger.Warn("Failed to clean up readiness file", "error", err, "path", config.ControllerReadinessProbePath)
	}

	dynamoClient, err := dynamodb.NewClient(config.AwsClientConfig, logger)
	if err != nil {
		return err
	}
	gethClient, err := geth.NewMultiHomingClient(config.EthClientConfig, gethcommon.Address{}, logger)
	if err != nil {
		logger.Error("Cannot create chain.Client", "err", err)
		return err
	}
	chainReader, err := eth.NewReader(logger, gethClient, config.EigenDADirectory, config.BLSOperatorStateRetrieverAddr, config.EigenDAServiceManagerAddr)
	if err != nil {
		return err
	}

	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metricsRegistry.MustRegister(collectors.NewGoCollector())

	logger.Infof("Starting metrics server at port %d", config.MetricsPort)
	addr := fmt.Sprintf(":%d", config.MetricsPort)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(
		metricsRegistry,
		promhttp.HandlerOpts{},
	))
	metricsServer := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	baseBlobMetadataStore := blobstore.NewBlobMetadataStore(
		dynamoClient,
		logger,
		config.DynamoDBTableName,
	)
	blobMetadataStore := blobstore.NewInstrumentedMetadataStore(baseBlobMetadataStore, blobstore.InstrumentedMetadataStoreConfig{
		ServiceName: "controller",
		Registry:    metricsRegistry,
		Backend:     blobstore.BackendDynamoDB,
	})

	controllerLivenessChan := make(chan healthcheck.HeartbeatMessage, 10)

	encoderClient, err := encoder.NewEncoderClientV2(config.EncodingManagerConfig.EncoderAddress)
	if err != nil {
		return fmt.Errorf("failed to create encoder client: %v", err)
	}
	encodingPool := workerpool.New(config.NumConcurrentEncodingRequests)
	encodingManagerBlobSet := controller.NewBlobSet()
	encodingManager, err := controller.NewEncodingManager(
		&config.EncodingManagerConfig,
		blobMetadataStore,
		encodingPool,
		encoderClient,
		chainReader,
		logger,
		metricsRegistry,
		encodingManagerBlobSet,
		controllerLivenessChan,
	)
	if err != nil {
		return fmt.Errorf("failed to create encoding manager: %v", err)
	}

	sigAgg, err := core.NewStdSignatureAggregator(logger, chainReader)
	if err != nil {
		return fmt.Errorf("failed to create signature aggregator: %v", err)
	}
	dispatcherPool := workerpool.New(config.NumConcurrentDispersalRequests)
	chainState := eth.NewChainState(chainReader, gethClient)
	var ics core.IndexedChainState
	if config.UseGraph {
		logger.Info("Using graph node")

		logger.Info("Connecting to subgraph", "url", config.ChainStateConfig.Endpoint)
		ics = thegraph.MakeIndexedChainState(config.ChainStateConfig, chainState, logger)
	} else {
		logger.Info("Using built-in indexer")
		rpcClient, err := rpc.Dial(config.EthClientConfig.RPCURLs[0])
		if err != nil {
			return err
		}
		idx, err := indexer.CreateNewIndexer(
			&config.IndexerConfig,
			gethClient,
			rpcClient,
			config.EigenDADirectory,
			logger,
		)
		if err != nil {
			return err
		}
		ics, err = indexer.NewIndexedChainState(chainState, idx)
		if err != nil {
			return err
		}
	}

	var requestSigner clients.DispersalRequestSigner
	if config.DisperserStoreChunksSigningDisabled {
		logger.Warn("StoreChunks() signing is disabled")
	} else {
		requestSigner, err = clients.NewDispersalRequestSigner(
			context.Background(),
			config.AwsClientConfig.Region,
			config.AwsClientConfig.EndpointURL,
			config.DisperserKMSKeyID)
		if err != nil {
			return fmt.Errorf("failed to create request signer: %v", err)
		}
	}

	nodeClientManager, err := controller.NewNodeClientManager(config.NodeClientCacheSize, requestSigner, logger)
	if err != nil {
		return fmt.Errorf("failed to create node client manager: %v", err)
	}
	beforeDispatch := func(blobKey corev2.BlobKey) error {
		encodingManagerBlobSet.RemoveBlob(blobKey)
		return nil
	}
	dispatcherBlobSet := controller.NewBlobSet()
	dispatcher, err := controller.NewDispatcher(
		&config.DispatcherConfig,
		blobMetadataStore,
		dispatcherPool,
		ics,
		sigAgg,
		nodeClientManager,
		logger,
		metricsRegistry,
		beforeDispatch,
		dispatcherBlobSet,
		controllerLivenessChan,
	)
	if err != nil {
		return fmt.Errorf("failed to create dispatcher: %v", err)
	}

	c := context.Background()

	err = controller.RecoverState(c, blobMetadataStore, logger)
	if err != nil {
		return fmt.Errorf("failed to recover state: %v", err)
	}

	err = encodingManager.Start(c)
	if err != nil {
		return fmt.Errorf("failed to start encoding manager: %v", err)
	}

	err = dispatcher.Start(c)
	if err != nil {
		return fmt.Errorf("failed to start dispatcher: %v", err)
	}

	go func() {
		err := metricsServer.ListenAndServe()
		if err != nil && !strings.Contains(err.Error(), "http: Server closed") {
			logger.Errorf("metrics metricsServer error: %v", err)
		}
	}()

	// Create readiness probe file once the controller starts successfully
	if _, err := os.Create(config.ControllerReadinessProbePath); err != nil {
		logger.Warn("Failed to create readiness file", "error", err, "path", config.ControllerReadinessProbePath)
	}

	if _, err := os.Create(config.ControllerHealthProbePath); err != nil {
		logger.Warn("Failed to create healthProbe file: %v", err)
	}

	// Start heartbeat monitor
	go func() {
		err := healthcheck.HeartbeatMonitor(
			config.ControllerHealthProbePath,
			controllerMaxStallDuration,
			controllerLivenessChan,
			logger,
		)
		if err != nil {
			logger.Warn("Heartbeat monitor exited with error", "err", err)
		}
	}()

	return nil
}
