/*
Copyright 2022 The Curve Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package curvefsdriver

import (
	"fmt"
	"k8s.io/klog/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultToolExampleConfPath   = "/curvefs/conf/tools.conf"
	defaultClientExampleConfPath = "/curvefs/conf/client.conf"
	toolPath                     = "/curvefs/tools/sbin/curvefs_tool"
	clientPath                   = "/curvefs/client/sbin/curve-fuse"
	cacheDirPrefix               = "/curvefs/client/data/cache/"
	PodMountBase                 = "/dfs"
	MountBase                    = "/var/lib/dfs"
)

type curvefsTool struct {
	toolParams map[string]string
}

func NewCurvefsTool() *curvefsTool {
	return &curvefsTool{toolParams: map[string]string{}}
}

func (ct *curvefsTool) CreateFs(
	volumeID string,
	capacity int64,
	params map[string]string,
) error {
	err := ct.validateCommonParams(params)
	if err != nil {
		return err
	}
	err = ct.validateCreateFsParams(params)
	if err != nil {
		return err
	}
	ct.toolParams["fsName"] = volumeID
	// todo: current capacity is not working
	// call curvefs_tool create-fs to create a fs
	createFsArgs := []string{"create-fs"}
	for k, v := range ct.toolParams {
		arg := fmt.Sprintf("-%s=%s", k, v)
		createFsArgs = append(createFsArgs, arg)
	}
	klog.V(1).Infof("create fs, createFsArgs: %v", createFsArgs)
	createFsCmd := exec.Command(toolPath, createFsArgs...)
	output, err := createFsCmd.CombinedOutput()
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"Curvefs_tool create-fs failed. cmd: %s %v, output: %s, err: %v",
			toolPath,
			createFsArgs,
			output,
			err,
		)
	}
	return nil
}

func (ct *curvefsTool) DeleteFs(volumeID string, params map[string]string) error {
	err := ct.validateCommonParams(params)
	if err != nil {
		return err
	}
	ct.toolParams["fsName"] = volumeID
	ct.toolParams["noconfirm"] = "1"
	// call curvefs_tool delete-fs to create a fs
	deleteFsArgs := []string{"delete-fs"}
	for k, v := range ct.toolParams {
		arg := fmt.Sprintf("-%s=%s", k, v)
		deleteFsArgs = append(deleteFsArgs, arg)
	}
	deleteFsCmd := exec.Command(toolPath, deleteFsArgs...)
	output, err := deleteFsCmd.CombinedOutput()
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"curvefs_tool delete-fs failed. cmd:%s %v, output: %s, err: %v",
			toolPath,
			deleteFsArgs,
			output,
			err,
		)
	}
	return nil
}

func (ct *curvefsTool) validateCommonParams(params map[string]string) error {
	if mdsAddr, ok := params["mdsAddr"]; ok {
		ct.toolParams["mdsAddr"] = mdsAddr
	} else {
		return status.Error(codes.InvalidArgument, "mdsAddr is missing")
	}
	if confPath, ok := params["toolConfPath"]; ok {
		ct.toolParams["confPath"] = confPath
	} else {
		ct.toolParams["confPath"] = defaultToolExampleConfPath
	}
	return nil
}

func (ct *curvefsTool) validateCreateFsParams(params map[string]string) error {
	if fsType, ok := params["fsType"]; ok {
		ct.toolParams["fsType"] = fsType
		enableSumInDir, ok := params["enableSumInDir"]
		if ok {
			ct.toolParams["enableSumInDir"] = enableSumInDir
		} else {
			ct.toolParams["enableSumInDir"] = "0"
		}
		if fsType == "s3" {
			s3Endpoint, ok1 := params["s3Endpoint"]
			s3AccessKey, ok2 := params["s3AccessKey"]
			s3SecretKey, ok3 := params["s3SecretKey"]
			s3Bucket, ok4 := params["s3Bucket"]
			if ok1 && ok2 && ok3 && ok4 {
				ct.toolParams["s3_endpoint"] = s3Endpoint
				ct.toolParams["s3_ak"] = s3AccessKey
				ct.toolParams["s3_sk"] = s3SecretKey
				ct.toolParams["s3_bucket_name"] = s3Bucket
			} else {
				return status.Error(codes.InvalidArgument, "s3Info is incomplete")
			}
		} else if fsType == "volume" {
			if backendVolName, ok := params["backendVolName"]; ok {
				ct.toolParams["volumeName"] = backendVolName
			} else {
				return status.Error(codes.InvalidArgument, "backendVolName is missing")
			}
			if backendVolSizeGB, ok := params["backendVolSizeGB"]; ok {
				backendVolSizeGBInt, err := strconv.ParseInt(backendVolSizeGB, 0, 64)
				if err != nil {
					return status.Error(codes.InvalidArgument, "backendVolSize is not integer")
				}
				if backendVolSizeGBInt < 10 {
					return status.Error(codes.InvalidArgument, "backendVolSize must larger than 10GB")
				}
				ct.toolParams["volumeSize"] = backendVolSizeGB
			} else {
				return status.Error(codes.InvalidArgument, "backendVolSize is missing")
			}
		} else {
			return status.Errorf(codes.InvalidArgument, "unsupported fsType %s", fsType)
		}
	} else {
		return status.Error(codes.InvalidArgument, "fsType is missing")
	}
	return nil
}

type curvefsMounter struct {
	mounterParams map[string]string
}

func NewCurvefsMounter() *curvefsMounter {
	return &curvefsMounter{mounterParams: map[string]string{}}
}

func (cm *curvefsMounter) MountFs(
	fsname string,
	mountPath string,
	params map[string]string,
	mountOption *csi.VolumeCapability_MountVolume,
	mountUUID string,
) error {
	err := cm.validateMountFsParams(params)
	if err != nil {
		return err
	}
	// mount options from storage class
	// copy and create new conf file with mount options override
	if mountOption != nil {
		confPath, err := cm.applyMountFlags(
			cm.mounterParams["conf"],
			mountOption.MountFlags,
			mountUUID,
		)
		if err != nil {
			return err
		}
		cm.mounterParams["conf"] = confPath
	}

	cm.mounterParams["fsname"] = fsname
	// curve-fuse -o default_permissions -o allow_other \
	//  -o conf=/etc/curvefs/client.conf -o fsname=testfs \
	//  -o fstype=s3  --mdsAddr=1.1.1.1 <mountpoint>
	var mountFsArgs []string
	doubleDashArgs := map[string]string{"mdsaddr": ""}
	extraPara := []string{"default_permissions", "allow_other"}
	for _, para := range extraPara {
		mountFsArgs = append(mountFsArgs, "-o")
		mountFsArgs = append(mountFsArgs, para)
	}

	for k, v := range cm.mounterParams {
		if _, ok := doubleDashArgs[k]; ok {
			arg := fmt.Sprintf("--%s=%s", k, v)
			mountFsArgs = append(mountFsArgs, arg)
		} else {
			mountFsArgs = append(mountFsArgs, "-o")
			arg := fmt.Sprintf("%s=%s", k, v)
			mountFsArgs = append(mountFsArgs, arg)
		}
	}

	mountFsArgs = append(mountFsArgs, mountPath)

	klog.V(3).Infof("curve-fuse mountFsArgs: %s", mountFsArgs)
	mountFsCmd := exec.Command(clientPath, mountFsArgs...)
	output, err := mountFsCmd.CombinedOutput()
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"curve-fuse mount failed. cmd: %s %v, output: %s, err: %v",
			clientPath,
			mountFsArgs,
			output,
			err,
		)
	}
	return nil
}

func (cm *curvefsMounter) UmountFs(targetPath string, mountUUID string) error {
	umountFsCmd := exec.Command("umount", targetPath)
	output, err := umountFsCmd.CombinedOutput()
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"umount %s failed. output: %s, err: %v",
			targetPath,
			output,
			err,
		)
	}
	// do cleanup, config file and cache dir
	if mountUUID != "" {
		confPath := defaultClientExampleConfPath + "." + mountUUID
		cacheDir := cacheDirPrefix + mountUUID
		go os.Remove(confPath)
		go os.RemoveAll(cacheDir)
	}
	return nil
}

// update the configuration file with the mount flags
func (cm *curvefsMounter) applyMountFlags(
	origConfPath string,
	mountFlags []string,
	mountUUID string,
) (string, error) {
	confPath := defaultClientExampleConfPath + "." + mountUUID

	// Step 1: Copy the original configuration file to a new file
	data, err := os.ReadFile(origConfPath)
	if err != nil {
		return "", status.Errorf(
			codes.Internal,
			"applyMountFlag: failed to read conf %v",
			origConfPath,
			err,
		)
	}
	err = os.WriteFile(confPath, data, 0644)
	if err != nil {
		return "", status.Errorf(
			codes.Internal,
			"applyMountFlag: failed to write new conf %v",
			confPath,
			err,
		)
	}

	// Step 2: Read the new configuration file
	data, err = os.ReadFile(confPath)
	if err != nil {
		return "", status.Errorf(
			codes.Internal,
			"applyMountFlag: failed to read new conf %v",
			confPath,
			err,
		)
	}

	// Step 3: Iterate over the mountFlags items
	lines := strings.Split(string(data), "\n")
	configMap := make(map[string]string)
	for _, line := range lines {
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		configMap[parts[0]] = parts[1]
	}

	cacheEnabled := false
	for _, flag := range mountFlags {
		parts := strings.SplitN(flag, "=", 2)
		if len(parts) == 2 {
			configMap[parts[0]] = parts[1]
			if parts[0] == "diskCache.diskCacheType" && (parts[1] == "2" || parts[1] == "1") {
				cacheEnabled = true
			}
		}
	}

	// Step 4: Write the updated configuration back to the new file
	var newData strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			newData.WriteString(line + "\n")
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if newValue, exists := configMap[parts[0]]; exists {
			if parts[0] == "disk_cache.cache_dir" {
				cacheDirs := strings.Split(newValue, ";")
				cacheDirsWithUUID := make([]string, len(cacheDirs))
				for i, cacheDir := range cacheDirs {
					cacheDirParts := strings.SplitN(cacheDir, ":", 2)
					if len(cacheDirParts) == 2 {
						cacheDirsWithUUID[i] = fmt.Sprintf("%s/%s:%s", cacheDirParts[0], mountUUID, cacheDirParts[1])
					} else {
						cacheDirsWithUUID[i] = fmt.Sprintf("%s/%s", cacheDirParts[0], mountUUID)
					}
				}
				newValue = strings.Join(cacheDirsWithUUID, ";")
			}
			newData.WriteString(fmt.Sprintf("%s=%s\n", parts[0], newValue))
			delete(configMap, parts[0])
		} else {
			newData.WriteString(line + "\n")
		}
	}

	// Write the remaining new configuration items
	for key, value := range configMap {
		newData.WriteString(fmt.Sprintf("%s=%s\n", key, value))
	}

	err = os.WriteFile(confPath, []byte(newData.String()), 0644)
	if err != nil {
		return "", status.Errorf(
			codes.Internal,
			"applyMountFlag: failed to write updated conf %v",
			confPath,
			err,
		)
	}

	if cacheEnabled {
		cacheDir := cacheDirPrefix + mountUUID
		if err := os.MkdirAll(cacheDir, 0777); err != nil {
			return "", err
		}
	}

	return confPath, nil
}

func (cm *curvefsMounter) validateMountFsParams(params map[string]string) error {
	if mdsAddr, ok := params["mdsAddr"]; ok {
		cm.mounterParams["mdsaddr"] = mdsAddr
	} else {
		return status.Error(codes.InvalidArgument, "mdsAddr is missing")
	}
	if confPath, ok := params["clientConfPath"]; ok {
		cm.mounterParams["conf"] = confPath
	} else {
		cm.mounterParams["conf"] = defaultClientExampleConfPath
	}
	if fsType, ok := params["fsType"]; ok {
		cm.mounterParams["fstype"] = fsType
	} else {
		return status.Error(codes.InvalidArgument, "fsType is missing")
	}
	return nil
}
