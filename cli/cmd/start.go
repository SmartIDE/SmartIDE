/*
 * @Author: jason chen (jasonchen@leansoftx.com, http://smallidea.cnblogs.com)
 * @Description:
 * @Date: 2021-11
 * @LastEditors: Jason Chen
 * @LastEditTime: 2022-10-24 14:39:12
 */
package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jinzhu/copier"
	"github.com/leansoftX/smartide-cli/cmd/server"
	smartideServer "github.com/leansoftX/smartide-cli/cmd/server"
	"github.com/leansoftX/smartide-cli/cmd/start"
	"github.com/leansoftX/smartide-cli/internal/apk/i18n"
	"github.com/leansoftX/smartide-cli/internal/biz/config"
	"github.com/leansoftX/smartide-cli/internal/biz/workspace"
	"github.com/leansoftX/smartide-cli/internal/dal"
	"github.com/leansoftX/smartide-cli/internal/model"

	"github.com/leansoftX/smartide-cli/internal/apk/appinsight"
	"github.com/leansoftX/smartide-cli/pkg/common"
	"github.com/leansoftX/smartide-cli/pkg/k8s"
	"gopkg.in/src-d/go-git.v4"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// var i18nInstance.Start = i18n.GetInstance().Start
var i18nInstance = i18n.GetInstance()

// yaml 文件的相对路径
var configYamlFileRelativePath string = model.CONST_Default_ConfigRelativeFilePath

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:     "start",
	Short:   i18nInstance.Start.Info_help_short,
	Long:    i18nInstance.Start.Info_help_long,
	Aliases: []string{"up"},
	Example: `  smartide start
  smartide start --workspaceid {workspaceid}
  smartide start <workspaceid>
  smartide start <actual git repo url>
  smartide start <actual git repo url> <templatetype> -T {typename}
  smartide start --host <host> --username <username> --password <password> --repourl <actual git repo url> --branch <branch name> --filepath <config file path>
  smartide start --host <host> --username <username> --password <password> --repourl <actual git repo url> --branch <branch name> --filepath <config file path> <templatetype> -T {typename}
  smartide start --host <hostid> <actual git repo url> 
  smartide start --host <hostid> <actual git repo url> <templatetype> -T {typename}
  smartide start --k8s <context> --repoUrl <actual git repo url> --branch master
  smartide start --k8s <context> <actual git repo url>`,
	PreRunE: preRun,
	RunE: func(cmd *cobra.Command, args []string) error {

		if apiHost, _ := cmd.Flags().GetString(server.Flags_ServerHost); apiHost != "" {
			wsURL := fmt.Sprint(strings.ReplaceAll(strings.ReplaceAll(apiHost, "https", "ws"), "http", "ws"), "/ws/smartide/ws")
			common.WebsocketStart(wsURL)
			token, _ := cmd.Flags().GetString(server.Flags_ServerToken)
			if token != "" {
				if workspaceIdStr := getWorkspaceIdFromFlagsOrArgs(cmd, args); strings.Contains(workspaceIdStr, "SWS") {
					if pid, err := workspace.GetParentId(workspaceIdStr, workspace.ActionEnum_Workspace_Start, token, apiHost); err == nil && pid > 0 {
						common.SmartIDELog.Ws_id = workspaceIdStr
						common.SmartIDELog.ParentId = pid
					}
				} else {
					if workspaceIdStr, _ := cmd.Flags().GetString(server.Flags_ServerWorkspaceid); workspaceIdStr != "" {
						if no, _ := workspace.GetWorkspaceNo(workspaceIdStr, token, apiHost); no != "" {
							if pid, err := workspace.GetParentId(no, workspace.ActionEnum_Workspace_Start, token, apiHost); err == nil && pid > 0 {
								common.SmartIDELog.Ws_id = no
								common.SmartIDELog.ParentId = pid
							}
						}
					}

				}
			}

		}
		//0. 提示文本
		common.SmartIDELog.Info(i18nInstance.Start.Info_start)

		//0.1. 从参数中获取结构体，并做基本的数据有效性校验
		common.SmartIDELog.Info(i18nInstance.Main.Info_workspace_loading)
		workspaceInfo, err := getWorkspaceFromCmd(cmd, args) // 获取 workspace 对象 ★★★★★
		entryptionKey4Workspace(workspaceInfo)               // 申明需要加密的文本
		common.CheckErrorFunc(err, func(err error) {
			mode, _ := cmd.Flags().GetString("mode")
			isModeServer := strings.ToLower(mode) == "server"
			if !isModeServer {
				return
			}
			if err != nil {
				common.SmartIDELog.Importance(err.Error())
				smartideServer.Feedback_Finish(server.FeedbackCommandEnum_Start, cmd, false, nil, workspaceInfo, err.Error(), "")
			}
		})

		// ai记录
		var trackEvent string
		for _, val := range args {
			trackEvent = trackEvent + " " + val
		}

		isUnforward, _ := cmd.Flags().GetBool("unforward")

		executeStartCmdFunc := func(yamlConfig config.SmartIdeConfig) {
			if config.GlobalSmartIdeConfig.IsInsightEnabled != config.IsInsightEnabledEnum_Enabled {
				common.SmartIDELog.Debug("Application Insights disabled")
				return
			}
			var imageNames []string
			for _, service := range yamlConfig.Workspace.Servcies {
				imageNames = append(imageNames, service.Image)
			}
			appinsight.SetTrack(cmd.Use, Version.TagName, trackEvent, string(workspaceInfo.Mode), strings.Join(imageNames, ","))
		}

		//1. 执行命令
		if workspaceInfo.Mode == workspace.WorkingMode_Local { //1.1. 本地模式
			workspaceInfo, err = start.ExecuteStartCmd(workspaceInfo, isUnforward, func(v string, d common.Docker) {}, executeStartCmdFunc, args, cmd)
			common.CheckError(err)

		} else if workspaceInfo.Mode == workspace.WorkingMode_K8s { //1.2. k8s 模式
			if workspaceInfo.CliRunningEnv == workspace.CliRunningEvnEnum_Server { //1.2.1. cli 在服务端运行
				k8sUtil, err := k8s.NewK8sUtilWithContent(workspaceInfo.K8sInfo.KubeConfigContent,
					workspaceInfo.K8sInfo.Context,
					workspaceInfo.K8sInfo.Namespace)
				common.CheckError(err)

				workspaceInfo, err = start.ExecuteK8sServerStartCmd(cmd, *k8sUtil, workspaceInfo, executeStartCmdFunc)
				common.CheckError(err)

			} else { //1.2.2. cli 在客户端运行
				k8sUtil, err := k8s.NewK8sUtil(workspaceInfo.K8sInfo.KubeConfigFilePath,
					workspaceInfo.K8sInfo.Context,
					workspaceInfo.K8sInfo.Namespace)
				common.CheckError(err)

				if workspaceInfo.CacheEnv == workspace.CacheEnvEnum_Server { //1.2.2.1. 远程工作区 本地加载
					workspaceInfo, err = start.ExecuteServerK8sStartByClientEnvCmd(workspaceInfo, executeStartCmdFunc)
					common.CheckError(err)

				} else { //1.2.2.2. 本地工作区，本地启动
					workspaceInfo, err = start.ExecuteK8sClientStartCmd(cmd, *k8sUtil, workspaceInfo, executeStartCmdFunc)
					common.CheckError(err)
				}

			}

		} else if workspaceInfo.Mode == workspace.WorkingMode_Remote { //1.3. 远程主机 模式

			if workspaceInfo.CliRunningEnv == workspace.CliRunningEvnEnum_Server { //1.3.1. cli 在服务端运行
				disabelGitClone := false
				if workspaceInfo.GitCloneRepoUrl == "" {
					disabelGitClone = true
				}
				workspaceInfo, err = start.ExecuteVmStartCmd(workspaceInfo, isUnforward, executeStartCmdFunc, cmd, args, disabelGitClone)
				common.CheckError(err)

			} else { //1.3.2. cli 在客户端运行

				if workspaceInfo.CacheEnv == workspace.CacheEnvEnum_Server { //1.3.2.1. 远程工作区 本地加载
					workspaceInfo, err = start.ExecuteServerVmStartByClientEnvCmd(workspaceInfo, executeStartCmdFunc)
					common.CheckError(err)

				} else { //1.3.2.2. 本地工作区，本地启动
					disabelGitClone := false
					if workspaceInfo.GitCloneRepoUrl == "" {
						disabelGitClone = true
					}
					workspaceInfo, err = start.ExecuteVmStartCmd(workspaceInfo, isUnforward, executeStartCmdFunc, cmd, args, disabelGitClone)
					common.CheckError(err)
				}

			}

		} else {
			return errors.New("暂不支持当前模式")
		}
		common.CheckError(err)

		//99. 结束
		//99.1. 文本
		common.SmartIDELog.Info(i18nInstance.Start.Info_end)
		if workspaceInfo.ConfigYaml.Workspace.DevContainer.IdeType == config.IdeTypeEnum_SDKOnly {
			common.SmartIDELog.Info("当前IDE环境没有提供WebIDE入口，请使用ssh连接工作区")
		}
		//99.2. 死循环进行驻守，允许端口转发 && 是在本地运行
		if !isUnforward && workspaceInfo.CliRunningEnv == workspace.CliRunningEnvEnum_Client {
			for {
				time.Sleep(time.Millisecond * 300)
			}

		}

		return nil
	},
}

// 运行前
func preRun(cmd *cobra.Command, args []string) error {
	kubeconfig, _ := cmd.Flags().GetString(flag_kubeconfig)
	context, _ := cmd.Flags().GetString(flag_k8s)
	mode, _ := cmd.Flags().GetString("mode")

	// 参数验证
	if mode == "server" {
		if kubeconfig != "" {
			common.SmartIDELog.Importance("server 模式下，--kubeconfig参数无效")
		}
	}
	if kubeconfig != "" && context == "" {
		return errors.New("k8s 参数为空！")
	}

	// 密钥加密显示
	gitPassword, _ := cmd.Flags().GetString(flag_gitpassword)
	if gitPassword != "" {
		common.SmartIDELog.AddEntryptionKeyWithReservePart(gitPassword)
	}
	remotePassword, _ := cmd.Flags().GetString(flag_password)
	if remotePassword != "" {
		common.SmartIDELog.AddEntryptionKey(remotePassword)
	}

	return nil
}

// 在某些情况下，参数填了也没有意义，比如指定了workspaceid，就不需要再填host
func checkFlagUnnecessary(fflags *pflag.FlagSet, flagName string, preFlagName string) {
	if fflags.Changed(flagName) {
		common.SmartIDELog.WarningF(i18nInstance.Main.Err_flag_value_invalid, preFlagName, flagName)
	}
}

// 检查参数是否填写
func checkFlagRequired(fflags *pflag.FlagSet, flagName string) error {
	if !fflags.Changed(flagName) {
		return fmt.Errorf(i18nInstance.Main.Err_flag_value_required, flagName)
	}
	return nil
}

var (
	flag_workspaceid = "workspaceid"
	flag_host        = "host"
	flag_port        = "port"
	flag_username    = "username"
	flag_password    = "password"
	flag_filepath    = "filepath"
	flag_repourl     = "repourl"
	flag_branch      = "branch"
	flag_k8s         = "k8s"
	flag_kubeconfig  = "kubeconfig"
	flag_gitpassword = "gitpassword"
)

// 获取工作区id
func getWorkspaceIdFromFlagsOrArgs(cmd *cobra.Command, args []string) string {
	fflags := cmd.Flags()

	// 从args 或者 flag 中获取值
	if len(args) > 0 { // 从args中加载
		tmpWorkspaceId := args[0]
		checkFlagUnnecessary(fflags, flag_workspaceid, tmpWorkspaceId)

		// 是否为数字，或者包含sw
		if common.IsNumber(tmpWorkspaceId) || strings.Index(strings.ToLower(tmpWorkspaceId), "ws") == 1 {
			return tmpWorkspaceId
		}
	}

	// 从 workspaceid 参数中加载
	if fflags.Changed(flag_workspaceid) { // 从flag中加载
		tmpWorkspaceId, err := fflags.GetString(flag_workspaceid)
		common.CheckError(err)
		tmpWorkspaceId = strings.TrimSpace(tmpWorkspaceId)

		// 是否为数字，或者包含sw
		if common.IsNumber(tmpWorkspaceId) || strings.Index(strings.ToLower(tmpWorkspaceId), "ws") == 1 {
			return tmpWorkspaceId
		}
	}

	// 从 serverworkspaceid 参数中获取
	if fflags.Changed("serverworkspaceid") { // 从flag中加载
		serverWorkspaceId, err := fflags.GetString("serverworkspaceid")
		common.CheckError(err)
		serverWorkspaceId = strings.TrimSpace(serverWorkspaceId)

		// 是否为数字，或者包含sw
		if common.IsNumber(serverWorkspaceId) || strings.Index(strings.ToLower(serverWorkspaceId), "ws") == 1 {
			return serverWorkspaceId
		}
	}

	return ""
}

// 从start command的flag、args中获取workspace
func getWorkspaceFromCmd(cmd *cobra.Command, args []string) (workspaceInfo workspace.WorkspaceInfo, err error) {
	fflags := cmd.Flags()
	//1.
	// 从args或flags中获取workspaceid，如 : smartide start 1
	workspaceIdStr := getWorkspaceIdFromFlagsOrArgs(cmd, args)

	// 加载workspace
	workspaceInfo = workspace.WorkspaceInfo{
		CliRunningEnv: workspace.CliRunningEnvEnum_Client,
		CacheEnv:      workspace.CacheEnvEnum_Local,
	}
	//1.1. 运行环境
	if value, _ := fflags.GetString("mode"); value != "" {
		if strings.ToLower(value) == "server" {
			workspaceInfo.CliRunningEnv = workspace.CliRunningEvnEnum_Server
		} else if strings.ToLower(value) == "pipeline" {
			workspaceInfo.CliRunningEnv = workspace.CliRunningEvnEnum_Pipeline
		}

	}
	if strings.Index(strings.ToLower(workspaceIdStr), "ws") == 1 {
		workspaceInfo.CacheEnv = workspace.CacheEnvEnum_Server
	} else if value, _ := fflags.GetString("serverworkspaceid"); value != "" {
		workspaceInfo.CacheEnv = workspace.CacheEnvEnum_Server
	}

	//1.2. 涉及到配置文件的改变
	if tmp, _ := fflags.GetString(flag_filepath); tmp != "" {
		configFilePath, err := fflags.GetString(flag_filepath)
		common.CheckError(err)

		if configFilePath != "" {
			if strings.Contains(configFilePath, "/") || strings.Contains(configFilePath, "\\") {
				workspaceInfo.ConfigFileRelativePath = configFilePath
			} else {
				workspaceInfo.ConfigFileRelativePath = ".ide/" + configFilePath
			}

		}
	}
	if workspaceInfo.ConfigFileRelativePath == "" { // 避免配置文件的路径为空
		workspaceInfo.ConfigFileRelativePath = model.CONST_Default_ConfigRelativeFilePath
	}
	if tmp, _ := fflags.GetString(flag_branch); tmp != "" {
		branch, err := fflags.GetString(flag_branch)
		common.CheckError(err)

		if branch != "" {
			workspaceInfo.Branch = branch
		}
	}

	//2. 获取基本的信息
	//2.1. 存储在 server 的工作区
	if workspaceInfo.CacheEnv == workspace.CacheEnvEnum_Server {
		auth := model.Auth{}
		if workspaceInfo.CliRunningEnv == workspace.CliRunningEvnEnum_Server {
			auth.Token, _ = fflags.GetString(server.Flags_ServerToken)
			auth.LoginUrl, _ = fflags.GetString(server.Flags_ServerHost)
			auth.UserName, _ = fflags.GetString(server.Flags_ServerUsername)
		} else {
			auth, err = workspace.GetCurrentUser()
			if err != nil {
				return
			}
		}

		if auth.IsNotNil() {
			// 从 api 获取 workspace
			var workspaceInfo_ *workspace.WorkspaceInfo
			workspaceInfo_, err = workspace.GetWorkspaceFromServer(auth, workspaceIdStr, workspaceInfo.CliRunningEnv)
			if workspaceInfo_ == nil {
				return workspace.WorkspaceInfo{}, fmt.Errorf("get workspace (%v) is null", workspaceIdStr)
			}
			workspaceInfo = *workspaceInfo_

			// 在 server 上运行的时候，git 用户名密码会从cmd中传递过来
			loadGitInfo4Workspace(&workspaceInfo, cmd, args)

			// 使用是否关联 server workspace 进行判断
			if workspaceInfo.ServerWorkSpace == nil {
				errMsg := ""
				if err != nil {
					errMsg = err.Error()
				}
				err = fmt.Errorf("没有查询到 (%v) 对应的工作区数据！"+errMsg, workspaceIdStr)
			} else {
				// 避免namespace为空
				if workspaceInfo.Mode == workspace.WorkingMode_K8s {
					if workspaceInfo.K8sInfo.Namespace == "" &&
						len(workspaceInfo.K8sInfo.TempK8sConfig.Workspace.Deployments) > 0 {
						workspaceInfo.K8sInfo.Namespace = workspaceInfo.K8sInfo.TempK8sConfig.Workspace.Deployments[0].Namespace
					}
				}
			}

		} else {
			err = fmt.Errorf("请运行smartide login命令登录！")
		}
		if err != nil {
			return
		}

	} else { //2.2. 存储在本地的工作区
		workspaceId, _ := strconv.Atoi(workspaceIdStr)
		common.CheckError(err)
		//1.1. 指定了从workspaceid，从sqlite中读取
		if workspaceId > 0 {
			checkFlagUnnecessary(fflags, flag_host, flag_workspaceid)
			checkFlagUnnecessary(fflags, flag_username, flag_workspaceid)
			checkFlagUnnecessary(fflags, flag_password, flag_workspaceid)

			err := getWorkspaceWithDbAndValid(workspaceId, &workspaceInfo) // 从数据库中加载
			if err != nil {
				return workspace.WorkspaceInfo{}, err
			}
			if workspaceInfo.IsNil() {
				return workspace.WorkspaceInfo{}, errors.New(i18nInstance.Main.Err_workspace_none)
			}

		} else { //1.2. 没有指定 workspaceid 的情况

			//1.2.0.1. 工作区类型
			workspaceInfo.Mode = workspace.WorkingMode_Local
			if getFlagValue(fflags, flag_host) != "" {
				workspaceInfo.Mode = workspace.WorkingMode_Remote
			} else if getFlagValue(fflags, "k8s") != "" {
				workspaceInfo.Mode = workspace.WorkingMode_K8s
			}

			//1.2.0.2. git 相关
			loadGitInfo4Workspace(&workspaceInfo, cmd, args)

			// 模式
			if workspaceInfo.Mode == workspace.WorkingMode_Remote { //1.2.1. vm 模式
				hostInfo, err := getRemoteAndValid(fflags)
				if err != nil {
					return workspace.WorkspaceInfo{}, err
				}
				workspaceInfo.Remote = *hostInfo

				// new 命令时，项目名称
				if cmd.Name() == "new" {
					if tmp, er := fflags.GetString("workspacename"); tmp == "" || er != nil {
						return workspace.WorkspaceInfo{}, errors.New("参数 workspacename 不为空！")
					}
					workspaceInfo.Name, _ = fflags.GetString("workspacename")
				}

				if workspaceInfo.Name == "" {
					workspaceInfo.Name = common.GetRepoName(workspaceInfo.GitCloneRepoUrl) + "-" + common.RandLowStr(3)
				}
				workspaceInfo.WorkingDirectoryPath = common.FilePahtJoin4Linux("~", model.CONST_REMOTE_REPO_ROOT, workspaceInfo.Name)

			} else if workspaceInfo.Mode == workspace.WorkingMode_K8s { //1.2.2. k8s模式
				workspaceInfo.K8sInfo.KubeConfigFilePath = getFlagValue(fflags, flag_kubeconfig)
				workspaceInfo.K8sInfo.Context = getFlagValue(fflags, flag_k8s)

			} else { //1.2.3. 本地模式
				// 当前目录
				pwd, err := os.Getwd()
				if err != nil {
					return workspace.WorkspaceInfo{}, err
				}
				workspaceInfo.Name = filepath.Base(pwd)

				// 本地模式下，不需要录入git库的克隆地址、分支
				//checkFlagUnnecessary(fflags, flag_repourl, "mode=local")
				checkFlagUnnecessary(fflags, flag_branch, "mode=local")

				// 本地模式下需要clone repo的情况：smartide start https://gitee.com/idcf-boat-house/boathouse-calculator.git
				if workspaceInfo.GitCloneRepoUrl != "" {
					common.SmartIDELog.Info(i18nInstance.Start.Info_git_clone)
					actualGtiRepoUrl := workspaceInfo.GitCloneRepoUrl
					if workspaceInfo.GitRepoAuthType == workspace.GitRepoAuthType_Basic {
						actualGtiRepoUrl, err =
							common.AddUsernamePassword4ActualGitRpoUrl(actualGtiRepoUrl, workspaceInfo.GitUserName, workspaceInfo.GitPassword)
						if err != nil {
							common.SmartIDELog.Warning(err.Error())
						}
					}
					clonedRepoDir, err := cloneRepo4LocalWithCommand(pwd, actualGtiRepoUrl) //
					common.CheckError(err)
					common.SmartIDELog.Info(i18nInstance.Common.Info_gitrepo_clone_done)

					workspaceInfo.WorkingDirectoryPath = clonedRepoDir
					os.Chdir(clonedRepoDir)
				} else {
					workspaceInfo.WorkingDirectoryPath = pwd
					workspaceInfo.GitCloneRepoUrl, _, err = getLocalGitRepoUrl(pwd) // 获取本地关联的repo url
					common.CheckError(err)
				}
			}

			// 从数据库中查找是否存在对应的workspace信息，主要是针对在当前目录再次start的情况
			conditionWorkingDir := ""
			if workspaceInfo.Mode == workspace.WorkingMode_Local {
				conditionWorkingDir = workspaceInfo.WorkingDirectoryPath
			}
			workspaceInfoDb, err := dal.GetSingleWorkspaceByParams(workspaceInfo.Mode, conditionWorkingDir,
				workspaceInfo.GitCloneRepoUrl, workspaceInfo.Branch, workspaceInfo.ConfigFileRelativePath,
				workspaceInfo.Remote.ID, workspaceInfo.Remote.Addr, workspaceInfo.Remote.UserName)
			common.CheckError(err)
			isNew := true
			if workspaceInfoDb.ID != "" {
				if workspaceInfo.CliRunningEnv != workspace.CliRunningEnvEnum_Client {
					common.SmartIDELog.Error("Not running on the server")
				}
				if workspaceInfo.Mode == workspace.WorkingMode_Remote {
					isEnable := ""
					fmt.Printf("远程工作区重复（可通过smartide list查看），是否创建新的工作区？（y | n）")
					fmt.Scanln(&isEnable)
					if strings.ToLower(isEnable) != "y" {
						isNew = false
						os.Exit(1)
					}

				} else {
					isNew = false
					copier.CopyWithOption(&workspaceInfo, workspaceInfoDb, copier.Option{IgnoreEmpty: false, DeepCopy: true})
				}

			}

			if isNew {
				if workspaceInfo.Mode != workspace.WorkingMode_K8s {
					// docker-compose 文件路径
					workspaceInfo.TempYamlFileAbsolutePath = workspaceInfo.GetTempDockerComposeFilePath()
				}

			}
		}
	}
	// 防止工作区名称为空的情况
	if workspaceInfo.Name == "" {
		workspaceInfo.Name = common.GetRepoName(workspaceInfo.GitCloneRepoUrl) + "-" + common.RandLowStr(3)
	}
	// addon,处理已经addon webterminal后再次运行不加aadon
	addon, _ := fflags.GetString("addon")
	if addon != "" {
		workspaceInfo.Addon = workspace.Addon{
			Type:     addon,
			IsEnable: true,
		}
	} else {
		// 仅对本地模式有效
		webterminServiceName := fmt.Sprintf("%v_smartide-webterminal", workspaceInfo.Name)
		if _, ok := workspaceInfo.TempDockerCompose.Services[webterminServiceName]; ok {
			workspaceInfo.Addon = workspace.Addon{
				Type:     "webterminal",
				IsEnable: true,
			}
		}
	}

	// 验证
	if workspaceInfo.Mode == workspace.WorkingMode_Remote {
		// path change
		workspaceInfo.ConfigFileRelativePath = common.FilePahtJoin4Linux(workspaceInfo.ConfigFileRelativePath)
		workspaceInfo.TempYamlFileAbsolutePath = common.FilePahtJoin4Linux(workspaceInfo.TempYamlFileAbsolutePath)
		workspaceInfo.WorkingDirectoryPath = common.FilePahtJoin4Linux(workspaceInfo.WorkingDirectoryPath)

		// 在远程模式下，首先验证远程服务器是否可以登录
		ssmRemote := common.SSHRemote{}
		common.SmartIDELog.InfoF(i18nInstance.Main.Info_ssh_connect_check, workspaceInfo.Remote.Addr, workspaceInfo.Remote.SSHPort)

		err = ssmRemote.CheckDail(workspaceInfo.Remote.Addr, workspaceInfo.Remote.SSHPort, workspaceInfo.Remote.UserName, workspaceInfo.Remote.Password, workspaceInfo.Remote.SSHKey)
		if err != nil {
			return workspaceInfo, err
		}
	}

	// 提示 加载对应的workspace
	if workspaceInfo.ID != "" && workspaceInfo.IsNotNil() {
		common.SmartIDELog.InfoF(i18nInstance.Start.Info_workspace_record_load, workspaceInfo.ID)

	}

	return workspaceInfo, err
}

func entryptionKey4Workspace(workspaceInfo workspace.WorkspaceInfo) {
	if workspaceInfo.Remote.Password != "" {
		common.SmartIDELog.AddEntryptionKey(workspaceInfo.Remote.Password)
	}
	if workspaceInfo.K8sInfo.KubeConfigContent != "" {
		common.SmartIDELog.AddEntryptionKeyWithReservePart(workspaceInfo.K8sInfo.KubeConfigContent)
	}
}

// 从命令行中加载 git 相关信息
func loadGitInfo4Workspace(workspaceInfo *workspace.WorkspaceInfo, cmd *cobra.Command, args []string) error {
	if workspaceInfo.GitCloneRepoUrl == "" {
		// 从args或flags中获取giturl，如 : smartide start https://github.com/idcf-boat-house/boathouse-calculator.git
		workspaceInfo.GitCloneRepoUrl = getGitUrlFromArgs(cmd, args)
	}

	// 用户名密码
	fflags := cmd.Flags()
	gitUsername := getFlagValue(fflags, "gitusername")
	gitPassword := getFlagValue(fflags, "gitpassword")
	if gitPassword != "" && gitUsername == "" {
		return errors.New("当参数 --gitpassword 不为空时，--gitusername 参数必须设置")
	} else if gitPassword == "" && gitUsername != "" {
		return errors.New("当参数 --gitusername 不为空时，--gitpassword 参数必须设置")
	}
	workspaceInfo.GitUserName = gitUsername
	workspaceInfo.GitPassword = gitPassword

	// 认证类型
	if strings.Index(workspaceInfo.GitCloneRepoUrl, "git") == 0 {
		workspaceInfo.GitRepoAuthType = workspace.GitRepoAuthType_SSH
	} else if workspaceInfo.GitUserName != "" && workspaceInfo.GitPassword != "" {
		workspaceInfo.GitRepoAuthType = workspace.GitRepoAuthType_Basic
	} else {
		workspaceInfo.GitRepoAuthType = workspace.GitRepoAuthType_Public
	}

	return nil
}

// 从文件夹中获取git repo url
func getLocalGitRepoUrl(pwd string) (gitRemmoteUrl, pathName string, err error) {
	// current directory
	fileInfo, err := os.Stat(pwd)
	if err != nil {
		return
	}
	pathName = fileInfo.Name()

	// git remote url
	gitRepo, err := git.PlainOpen(pwd)
	if err != nil {
		return
	}

	gitRemote, err := gitRepo.Remote("origin")
	if err != nil {
		return
	}
	gitRemmoteUrl = gitRemote.Config().URLs[0]

	return gitRemmoteUrl, pathName, nil
}

// 获取命令行参数的值
func getFlagValue(fflags *pflag.FlagSet, flag string) string {
	value, err := fflags.GetString(flag)
	if err != nil {
		if strings.Contains(err.Error(), "flag accessed but not defined:") { // 错误判断，不需要双语
			common.SmartIDELog.Debug(err.Error())
		} else {
			common.SmartIDELog.Error(err)
		}

	}
	return value
}

// 友好的错误
type FriendlyError struct {
	Err error
}

func (e *FriendlyError) Error() string {
	return e.Err.Error()
}

// 从数据库中加载工作区信息，并进行验证
func getWorkspaceWithDbAndValid(workspaceId int, originWorkspaceInfo *workspace.WorkspaceInfo) error {

	workspaceInfo_, err := dal.GetSingleWorkspace(int(workspaceId))
	if err != nil {
		return err
	}
	workspaceInfo_.CacheEnv = workspace.CacheEnvEnum_Local
	workspaceInfo_.CliRunningEnv = originWorkspaceInfo.CliRunningEnv

	// 验证在workspace中是否存在
	if workspaceInfo_.IsNil() {
		msg := fmt.Sprintf(i18nInstance.Main.Err_workspace_none)
		err = errors.New(msg)
		return err
	}

	// 如果扩展信息为空（通常是旧项目导致的），就从docker-compose文件中加载扩展信息
	if workspaceInfo_.Extend.IsNil() &&
		workspaceInfo_.ConfigYaml.IsNotNil() && workspaceInfo_.TempDockerCompose.IsNotNil() {
		workspaceInfo_.Extend = workspaceInfo_.GetWorkspaceExtend()

	}

	// 当前目录下不需要再次录入workspaceid
	twd, _ := os.Getwd()
	if workspaceInfo_.Mode == workspace.WorkingMode_Local && twd == workspaceInfo_.WorkingDirectoryPath {
		common.SmartIDELog.Warning(i18nInstance.Main.Err_flag_value_invalid2)

	}

	// 临时compose文件路径
	if workspaceInfo_.TempYamlFileAbsolutePath == "" {
		workspaceInfo_.TempYamlFileAbsolutePath = workspaceInfo_.GetTempDockerComposeFilePath()
	}

	// 验证数据
	err = workspaceInfo_.Valid()

	// 赋值
	copier.CopyWithOption(originWorkspaceInfo, &workspaceInfo_, copier.Option{IgnoreEmpty: true, DeepCopy: true})

	return err
}

// 根据参数，从数据库或者其他参数中加载远程服务器的信息
func getRemoteAndValid(fflags *pflag.FlagSet) (remoteInfo *workspace.RemoteInfo, err error) {

	host, _ := fflags.GetString(flag_host)
	userName, _ := fflags.GetString(flag_username)

	// 指定了host信息，尝试从数据库中加载
	if common.IsNumber(host) {
		remoteId, err := strconv.Atoi(host)
		if err != nil {
			return nil, err
		}
		remoteInfo, err = dal.GetRemoteById(remoteId)
		if err != nil {
			return nil, err
		}

		if remoteInfo == nil {
			return nil, errors.New(i18nInstance.Host.Err_host_data_not_exit)
		}
	} else {
		remoteInfo, err = dal.GetRemoteByHost(host, userName)

		// 如果在sqlite中有缓存数据，就不需要用户名、密码
		if remoteInfo != nil && remoteInfo.UserName == flag_username {
			checkFlagUnnecessary(fflags, flag_username, flag_host)
			checkFlagUnnecessary(fflags, flag_password, flag_host)
		}
	}

	// 从参数中加载
	if remoteInfo == nil {
		remoteInfo = &workspace.RemoteInfo{}
		//  必填字段验证
		err = checkFlagRequired(fflags, flag_host)
		if err != nil {
			return remoteInfo, &FriendlyError{Err: err}
		}
		err = checkFlagRequired(fflags, flag_username)
		if err != nil {
			return remoteInfo, &FriendlyError{Err: err}
		}

		remoteInfo.Addr = host
		remoteInfo.UserName = getFlagValue(fflags, flag_username)
		remoteInfo.SSHPort, err = fflags.GetInt(flag_port) //strconv.Atoi(getFlagValue(fflags, flag_port))
		common.CheckError(err)
		if remoteInfo.SSHPort <= 0 {
			remoteInfo.SSHPort = model.CONST_Container_SSHPort
		}
		// 认证类型
		if fflags.Changed(flag_password) {
			remoteInfo.Password = getFlagValue(fflags, flag_password)
			remoteInfo.AuthType = workspace.RemoteAuthType_Password
		} else {
			remoteInfo.AuthType = workspace.RemoteAuthType_SSH
		}

	}

	return remoteInfo, err
}

func init() {

	startCmd.Flags().Int32P("workspaceid", "w", 0, i18nInstance.Remove.Info_flag_workspaceid)
	startCmd.Flags().BoolP("unforward", "", false, "是否禁止端口转发")

	startCmd.Flags().StringP("host", "o", "", i18nInstance.Start.Info_help_flag_host)
	startCmd.Flags().IntP("port", "p", 22, i18nInstance.Start.Info_help_flag_port)
	startCmd.Flags().StringP("username", "u", "", i18nInstance.Start.Info_help_flag_username)
	startCmd.Flags().StringP("password", "t", "", i18nInstance.Start.Info_help_flag_password)

	startCmd.Flags().StringP("repourl", "r", "", i18nInstance.Start.Info_help_flag_repourl)
	startCmd.Flags().StringP("branch", "b", "", i18nInstance.Start.Info_help_flag_branch)
	startCmd.Flags().StringP("gitusername", "", "", "访问当前git库的用户信息")
	startCmd.Flags().StringP("gitpassword", "", "", "对当前git库拥有访问权限的令牌")

	startCmd.Flags().StringP("callback-api-address", "", "", i18nInstance.Start.Info_help_flag_callback_api_address)
	startCmd.Flags().StringVarP(&configYamlFileRelativePath, "filepath", "f", "", i18nInstance.Start.Info_help_flag_filepath)

	startCmd.Flags().StringP("k8s", "k", "", i18nInstance.Start.Info_help_flag_k8s)
	startCmd.Flags().StringP("kubeconfig", "", "", "自定义 kube config 文件的本地路径")
	// startCmd.Flags().StringP("namespace", "n", "", i18nInstance.Start.Info_help_flag_k8s_namespace)
	startCmd.Flags().StringP("serverownerguid", "g", "", i18nInstance.Start.Info_help_flag_ownerguid)
	startCmd.Flags().StringP("addon", "", "", "addon webterminal")
	startCmd.Flags().StringP("type", "T", "", i18nInstance.New.Info_help_flag_type)
}

// 获取参数gitUrl
func getGitUrlFromArgs(cmd *cobra.Command, args []string) string {
	// 从args中获取值
	var gitUrl string = ""
	if len(args) > 0 { // 从args中加载
		str := args[0]
		if strings.Index(str, "git@") == 0 || strings.Index(str, "http://") == 0 || strings.Index(str, "https://") == 0 {
			gitUrl = str
		}
	}

	//
	if gitUrl == "" {
		gitUrl = getFlagValue(cmd.Flags(), flag_repourl)
	}

	return gitUrl
}

// 直接调用git命令进行git clone
func cloneRepo4LocalWithCommand(rootDir string, actualGitRepoUrl string) (string, error) {
	repoName := common.GetRepoName(actualGitRepoUrl)
	repoPath := common.PathJoin(rootDir, repoName)

	var execCommand *exec.Cmd
	command := "git clone " + actualGitRepoUrl
	switch runtime.GOOS {
	case "windows":
		execCommand = exec.Command("powershell", "/c", command)
	case "darwin":
		execCommand = exec.Command("bash", "-c", command)
	case "linux":
		execCommand = exec.Command("bash", "-c", command)
	default:
		common.SmartIDELog.Error("can not support current os")
	}

	// run
	execCommand.Stdout = os.Stdout
	execCommand.Stderr = os.Stderr
	err := execCommand.Run()

	return repoPath, err
}

/* // clone repos for local mode
func cloneRepo4Local(rootDir string, gitUrl string) string {
	repoName := common.GetRepoName(gitUrl)
	repoPath := common.PathJoin(rootDir, repoName)
	options := &git.CloneOptions{
		URL:      gitUrl,
		Progress: os.Stdout,
	}
	if strings.Index(gitUrl, "git@") == 0 {
		var publicKey *ssh.PublicKeys

		// current user directory -> id_rsa file path
		currentUser, err := user.Current()
		if err != nil {
			log.Fatalf(err.Error())
		}
		homeDirectory := currentUser.HomeDir
		idRsaFilePath := ""
		if runtime.GOOS == "windows" {
			idRsaFilePath = common.PathJoin(homeDirectory, "\\.ssh\\id_rsa")
		} else {
			idRsaFilePath = common.PathJoin(homeDirectory, "/.ssh/id_rsa")
		}

		//
		publicKey, keyError := ssh.NewPublicKeysFromFile("git", idRsaFilePath, "")
		if keyError != nil {
			common.CheckError(keyError)
		}
		//ignore known_hosts check to fix gitee.com known_hosts lack issue
		publicKey.HostKeyCallback = ssh2.InsecureIgnoreHostKey()
		options.Auth = publicKey
	}
	_, err := git.PlainClone(repoPath, false, options)
	if err != nil {
		if err.Error() == "repository already exists" {
			message := fmt.Sprintf(i18nInstance.Main.Err_git_clone_folder_exist, repoName)
			common.SmartIDELog.Error(message)
		} else {
			common.CheckError(err)
		}
	}
	return repoPath
}
*/
