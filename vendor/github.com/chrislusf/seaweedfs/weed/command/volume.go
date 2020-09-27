package command

import (
	"fmt"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
	"google.golang.org/grpc"

	"github.com/chrislusf/seaweedfs/weed/util/grace"

	"github.com/chrislusf/seaweedfs/weed/pb"
	"github.com/chrislusf/seaweedfs/weed/security"
	"github.com/chrislusf/seaweedfs/weed/util/httpdown"

	"google.golang.org/grpc/reflection"

	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb/volume_server_pb"
	"github.com/chrislusf/seaweedfs/weed/server"
	"github.com/chrislusf/seaweedfs/weed/storage"
	"github.com/chrislusf/seaweedfs/weed/util"
)

var (
	v VolumeServerOptions
)

type VolumeServerOptions struct {
	port                  *int
	publicPort            *int
	folders               []string
	folderMaxLimits       []int
	ip                    *string
	publicUrl             *string
	bindIp                *string
	masters               *string
	idleConnectionTimeout *int
	dataCenter            *string
	rack                  *string
	whiteList             []string
	indexType             *string
	fixJpgOrientation     *bool
	readRedirect          *bool
	cpuProfile            *string
	memProfile            *string
	compactionMBPerSecond *int
	fileSizeLimitMB       *int
	minFreeSpacePercents  []float32
	pprof                 *bool
	// pulseSeconds          *int
}

func init() {
	cmdVolume.Run = runVolume // break init cycle
	v.port = cmdVolume.Flag.Int("port", 8080, "http listen port")
	v.publicPort = cmdVolume.Flag.Int("port.public", 0, "port opened to public")
	v.ip = cmdVolume.Flag.String("ip", util.DetectedHostAddress(), "ip or server name")
	v.publicUrl = cmdVolume.Flag.String("publicUrl", "", "Publicly accessible address")
	v.bindIp = cmdVolume.Flag.String("ip.bind", "0.0.0.0", "ip address to bind to")
	v.masters = cmdVolume.Flag.String("mserver", "localhost:9333", "comma-separated master servers")
	// v.pulseSeconds = cmdVolume.Flag.Int("pulseSeconds", 5, "number of seconds between heartbeats, must be smaller than or equal to the master's setting")
	v.idleConnectionTimeout = cmdVolume.Flag.Int("idleTimeout", 30, "connection idle seconds")
	v.dataCenter = cmdVolume.Flag.String("dataCenter", "", "current volume server's data center name")
	v.rack = cmdVolume.Flag.String("rack", "", "current volume server's rack name")
	v.indexType = cmdVolume.Flag.String("index", "memory", "Choose [memory|leveldb|leveldbMedium|leveldbLarge] mode for memory~performance balance.")
	v.fixJpgOrientation = cmdVolume.Flag.Bool("images.fix.orientation", false, "Adjust jpg orientation when uploading.")
	v.readRedirect = cmdVolume.Flag.Bool("read.redirect", true, "Redirect moved or non-local volumes.")
	v.cpuProfile = cmdVolume.Flag.String("cpuprofile", "", "cpu profile output file")
	v.memProfile = cmdVolume.Flag.String("memprofile", "", "memory profile output file")
	v.compactionMBPerSecond = cmdVolume.Flag.Int("compactionMBps", 0, "limit background compaction or copying speed in mega bytes per second")
	v.fileSizeLimitMB = cmdVolume.Flag.Int("fileSizeLimitMB", 256, "limit file size to avoid out of memory")
	v.pprof = cmdVolume.Flag.Bool("pprof", false, "enable pprof http handlers. precludes --memprofile and --cpuprofile")
}

var cmdVolume = &Command{
	UsageLine: "volume -port=8080 -dir=/tmp -max=5 -ip=server_name -mserver=localhost:9333",
	Short:     "start a volume server",
	Long: `start a volume server to provide storage spaces

  `,
}

var (
	volumeFolders         = cmdVolume.Flag.String("dir", os.TempDir(), "directories to store data files. dir[,dir]...")
	maxVolumeCounts       = cmdVolume.Flag.String("max", "8", "maximum numbers of volumes, count[,count]... If set to zero, the limit will be auto configured.")
	volumeWhiteListOption = cmdVolume.Flag.String("whiteList", "", "comma separated Ip addresses having write permission. No limit if empty.")
	minFreeSpacePercent   = cmdVolume.Flag.String("minFreeSpacePercent", "1", "minimum free disk space (default to 1%). Low disk space will mark all volumes as ReadOnly.")
)

func runVolume(cmd *Command, args []string) bool {

	util.LoadConfiguration("security", false)

	runtime.GOMAXPROCS(runtime.NumCPU())

	// If --pprof is set we assume the caller wants to be able to collect
	// cpu and memory profiles via go tool pprof
	if !*v.pprof {
		grace.SetupProfiling(*v.cpuProfile, *v.memProfile)
	}

	v.startVolumeServer(*volumeFolders, *maxVolumeCounts, *volumeWhiteListOption, *minFreeSpacePercent)

	return true
}

func (v VolumeServerOptions) startVolumeServer(volumeFolders, maxVolumeCounts, volumeWhiteListOption, minFreeSpacePercent string) {

	// Set multiple folders and each folder's max volume count limit'
	v.folders = strings.Split(volumeFolders, ",")
	for _, folder := range v.folders {
		if err := util.TestFolderWritable(util.ResolvePath(folder)); err != nil {
			glog.Fatalf("Check Data Folder(-dir) Writable %s : %s", folder, err)
		}
	}

	// set max
	maxCountStrings := strings.Split(maxVolumeCounts, ",")
	for _, maxString := range maxCountStrings {
		if max, e := strconv.Atoi(maxString); e == nil {
			v.folderMaxLimits = append(v.folderMaxLimits, max)
		} else {
			glog.Fatalf("The max specified in -max not a valid number %s", maxString)
		}
	}
	if len(v.folders) != len(v.folderMaxLimits) {
		glog.Fatalf("%d directories by -dir, but only %d max is set by -max", len(v.folders), len(v.folderMaxLimits))
	}

	// set minFreeSpacePercent
	minFreeSpacePercentStrings := strings.Split(minFreeSpacePercent, ",")
	for _, freeString := range minFreeSpacePercentStrings {
		if value, e := strconv.ParseFloat(freeString, 32); e == nil {
			v.minFreeSpacePercents = append(v.minFreeSpacePercents, float32(value))
		} else {
			glog.Fatalf("The value specified in -minFreeSpacePercent not a valid value %s", freeString)
		}
	}
	if len(v.minFreeSpacePercents) == 1 && len(v.folders) > 1 {
		for i := 0; i < len(v.folders)-1; i++ {
			v.minFreeSpacePercents = append(v.minFreeSpacePercents, v.minFreeSpacePercents[0])
		}
	}
	if len(v.folders) != len(v.minFreeSpacePercents) {
		glog.Fatalf("%d directories by -dir, but only %d minFreeSpacePercent is set by -minFreeSpacePercent", len(v.folders), len(v.minFreeSpacePercents))
	}

	// security related white list configuration
	if volumeWhiteListOption != "" {
		v.whiteList = strings.Split(volumeWhiteListOption, ",")
	}

	if *v.ip == "" {
		*v.ip = util.DetectedHostAddress()
		glog.V(0).Infof("detected volume server ip address: %v", *v.ip)
	}

	if *v.publicPort == 0 {
		*v.publicPort = *v.port
	}
	if *v.publicUrl == "" {
		*v.publicUrl = *v.ip + ":" + strconv.Itoa(*v.publicPort)
	}

	volumeMux := http.NewServeMux()
	publicVolumeMux := volumeMux
	if v.isSeparatedPublicPort() {
		publicVolumeMux = http.NewServeMux()
	}

	if *v.pprof {
		volumeMux.HandleFunc("/debug/pprof/", httppprof.Index)
		volumeMux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
		volumeMux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
		volumeMux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
		volumeMux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
	}

	volumeNeedleMapKind := storage.NeedleMapInMemory
	switch *v.indexType {
	case "leveldb":
		volumeNeedleMapKind = storage.NeedleMapLevelDb
	case "leveldbMedium":
		volumeNeedleMapKind = storage.NeedleMapLevelDbMedium
	case "leveldbLarge":
		volumeNeedleMapKind = storage.NeedleMapLevelDbLarge
	}

	masters := *v.masters

	volumeServer := weed_server.NewVolumeServer(volumeMux, publicVolumeMux,
		*v.ip, *v.port, *v.publicUrl,
		v.folders, v.folderMaxLimits, v.minFreeSpacePercents,
		volumeNeedleMapKind,
		strings.Split(masters, ","), 5, *v.dataCenter, *v.rack,
		v.whiteList,
		*v.fixJpgOrientation, *v.readRedirect,
		*v.compactionMBPerSecond,
		*v.fileSizeLimitMB,
	)

	// starting grpc server
	grpcS := v.startGrpcService(volumeServer)

	// starting public http server
	var publicHttpDown httpdown.Server
	if v.isSeparatedPublicPort() {
		publicHttpDown = v.startPublicHttpService(publicVolumeMux)
		if nil == publicHttpDown {
			glog.Fatalf("start public http service failed")
		}
	}

	// starting the cluster http server
	clusterHttpServer := v.startClusterHttpService(volumeMux)

	stopChain := make(chan struct{})
	grace.OnInterrupt(func() {
		fmt.Println("volume server has be killed")
		var startTime time.Time

		// firstly, stop the public http service to prevent from receiving new user request
		if nil != publicHttpDown {
			startTime = time.Now()
			if err := publicHttpDown.Stop(); err != nil {
				glog.Warningf("stop the public http server failed, %v", err)
			}
			delta := time.Now().Sub(startTime).Nanoseconds() / 1e6
			glog.V(0).Infof("stop public http server, elapsed %dms", delta)
		}

		startTime = time.Now()
		if err := clusterHttpServer.Stop(); err != nil {
			glog.Warningf("stop the cluster http server failed, %v", err)
		}
		delta := time.Now().Sub(startTime).Nanoseconds() / 1e6
		glog.V(0).Infof("graceful stop cluster http server, elapsed [%d]", delta)

		startTime = time.Now()
		grpcS.GracefulStop()
		delta = time.Now().Sub(startTime).Nanoseconds() / 1e6
		glog.V(0).Infof("graceful stop gRPC, elapsed [%d]", delta)

		startTime = time.Now()
		volumeServer.Shutdown()
		delta = time.Now().Sub(startTime).Nanoseconds() / 1e6
		glog.V(0).Infof("stop volume server, elapsed [%d]", delta)

		pprof.StopCPUProfile()

		close(stopChain) // notify exit
	})

	select {
	case <-stopChain:
	}
	glog.Warningf("the volume server exit.")
}

// check whether configure the public port
func (v VolumeServerOptions) isSeparatedPublicPort() bool {
	return *v.publicPort != *v.port
}

func (v VolumeServerOptions) startGrpcService(vs volume_server_pb.VolumeServerServer) *grpc.Server {
	grpcPort := *v.port + 10000
	grpcL, err := util.NewListener(*v.bindIp+":"+strconv.Itoa(grpcPort), 0)
	if err != nil {
		glog.Fatalf("failed to listen on grpc port %d: %v", grpcPort, err)
	}
	grpcS := pb.NewGrpcServer(security.LoadServerTLS(util.GetViper(), "grpc.volume"))
	volume_server_pb.RegisterVolumeServerServer(grpcS, vs)
	reflection.Register(grpcS)
	go func() {
		if err := grpcS.Serve(grpcL); err != nil {
			glog.Fatalf("start gRPC service failed, %s", err)
		}
	}()
	return grpcS
}

func (v VolumeServerOptions) startPublicHttpService(handler http.Handler) httpdown.Server {
	publicListeningAddress := *v.bindIp + ":" + strconv.Itoa(*v.publicPort)
	glog.V(0).Infoln("Start Seaweed volume server", util.Version(), "public at", publicListeningAddress)
	publicListener, e := util.NewListener(publicListeningAddress, time.Duration(*v.idleConnectionTimeout)*time.Second)
	if e != nil {
		glog.Fatalf("Volume server listener error:%v", e)
	}

	pubHttp := httpdown.HTTP{StopTimeout: 5 * time.Minute, KillTimeout: 5 * time.Minute}
	publicHttpDown := pubHttp.Serve(&http.Server{Handler: handler}, publicListener)
	go func() {
		if err := publicHttpDown.Wait(); err != nil {
			glog.Errorf("public http down wait failed, %v", err)
		}
	}()

	return publicHttpDown
}

func (v VolumeServerOptions) startClusterHttpService(handler http.Handler) httpdown.Server {
	var (
		certFile, keyFile string
	)
	if viper.GetString("https.volume.key") != "" {
		certFile = viper.GetString("https.volume.cert")
		keyFile = viper.GetString("https.volume.key")
	}

	listeningAddress := *v.bindIp + ":" + strconv.Itoa(*v.port)
	glog.V(0).Infof("Start Seaweed volume server %s at %s", util.Version(), listeningAddress)
	listener, e := util.NewListener(listeningAddress, time.Duration(*v.idleConnectionTimeout)*time.Second)
	if e != nil {
		glog.Fatalf("Volume server listener error:%v", e)
	}

	httpDown := httpdown.HTTP{
		KillTimeout: 5 * time.Minute,
		StopTimeout: 5 * time.Minute,
		CertFile:    certFile,
		KeyFile:     keyFile}
	clusterHttpServer := httpDown.Serve(&http.Server{Handler: handler}, listener)
	go func() {
		if e := clusterHttpServer.Wait(); e != nil {
			glog.Fatalf("Volume server fail to serve: %v", e)
		}
	}()
	return clusterHttpServer
}
