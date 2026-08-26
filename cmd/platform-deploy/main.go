package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/selfdeploy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "部署失败：", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("用法：platform-deploy <render|preflight|deploy|status|rollback> [参数]")
	}
	action := strings.TrimSpace(os.Args[1])
	flags := flag.NewFlagSet("platform-deploy "+action, flag.ContinueOnError)
	configPath := flags.String("config", "deploy/kubernetes/deploy.yaml", "部署配置文件")
	tag := flags.String("tag", "", "本次不可变镜像 Tag；留空自动生成")
	image := flags.String("image", "registry.invalid/ops-deploy-platform:validation", "render 操作使用的完整镜像地址")
	skipBuild := flags.Bool("skip-build", false, "跳过本地构建与推送，直接使用目标 Tag")
	timeout := flags.Duration("timeout", 90*time.Minute, "整体操作超时")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	config, err := selfdeploy.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	deployer := selfdeploy.New(config, os.Stdout, os.Stderr)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	switch action {
	case "render":
		if strings.TrimSpace(*image) == "" || strings.ContainsAny(*image, " \t\r\n") {
			return errors.New("render 需要合法的 --image")
		}
		var manifest []byte
		manifest, err = deployer.RenderManifest(strings.TrimSpace(*image))
		if err == nil {
			_, err = os.Stdout.Write(manifest)
		}
	case "preflight":
		_, err = deployer.Preflight(ctx, true)
	case "deploy":
		err = deployer.Deploy(ctx, *tag, *skipBuild)
	case "status":
		err = deployer.Status(ctx)
	case "rollback":
		err = deployer.Rollback(ctx)
	default:
		return fmt.Errorf("未知操作 %q；支持 render、preflight、deploy、status、rollback", action)
	}
	return err
}
