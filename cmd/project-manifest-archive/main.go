package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/projectarchive"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("用法: project-manifest-archive sync|plan|apply|clean")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	inventoryPath := flags.String("inventory", "deploy/project-archives.yaml", "项目与 kube context 清单")
	root := flags.String("root", "../已部署项目归档", "归档根目录")
	project := flags.String("project", "", "项目标识")
	environment := flags.String("environment", "", "环境标识")
	namespace := flags.String("namespace", "", "Helm release namespace")
	release := flags.String("release", "", "Helm release name")
	confirmation := flags.String("confirm", "", "生产变更确认字符串")
	concurrency := flags.Int("concurrency", 4, "归档并发数")
	timeout := flags.Duration("timeout", 30*time.Minute, "单条命令超时")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	service := projectarchive.Service{Root: *root, Concurrency: *concurrency, Timeout: *timeout, Stdout: os.Stdout, Stderr: os.Stderr}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	switch command {
	case "sync":
		inventory, err := projectarchive.LoadInventory(*inventoryPath)
		if err != nil {
			return err
		}
		return service.Sync(ctx, inventory)
	case "clean":
		inventory, err := projectarchive.LoadInventory(*inventoryPath)
		if err != nil {
			return err
		}
		return service.CleanSnapshots(inventory, *confirmation)
	case "plan":
		return service.Plan(ctx, *project, *environment, *namespace, *release)
	case "apply":
		return service.Apply(ctx, *project, *environment, *namespace, *release, *confirmation)
	default:
		return fmt.Errorf("未知命令 %q；仅支持 sync、plan、apply、clean", command)
	}
}
