package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
)

const metaURL = "https://launcher-meta.modrinth.com/forge/v0/manifest.json"

// 从 installer jar 解压 version.json 到 outDir
func extractVersionJson(jarPath, outDir string) error {
	zipReader, err := zip.OpenReader(jarPath)
	if err != nil {
		return err
	}
	defer zipReader.Close()
	for _, f := range zipReader.File {
		if f.Name == "version.json" {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			outPath := filepath.Join(outDir, "version.json")
			outFile, err := os.Create(outPath)
			if err != nil {
				return err
			}
			defer outFile.Close()
			_, err = io.Copy(outFile, rc)
			if err != nil {
				return err
			}
			fmt.Printf("Extracted version.json to %s\n", outPath)
			return nil
		}
	}
	return fmt.Errorf("version.json not found in %s", jarPath)
}

// 从 installer jar 提取 install_profile.json
func extractInstallProfile(jarPath, outPath string) error {
	zipReader, err := zip.OpenReader(jarPath)
	if err != nil {
		return err
	}
	defer zipReader.Close()
	
	// 尝试多个可能的文件名
	possibleNames := []string{"install_profile.json", "installer_profile.json", "profile.json"}
	
	for _, name := range possibleNames {
		for _, f := range zipReader.File {
			if f.Name == name || strings.HasSuffix(f.Name, "/"+name) {
				rc, err := f.Open()
				if err != nil {
					continue
				}
				defer rc.Close()
				
				outFile, err := os.Create(outPath)
				if err != nil {
					return err
				}
				defer outFile.Close()
				
				_, err = io.Copy(outFile, rc)
				if err != nil {
					return err
				}
				
				fmt.Printf("Extracted %s to %s\n", f.Name, outPath)
				return nil
			}
		}
	}
	
	return fmt.Errorf("install_profile.json not found in %s", jarPath)
}

// 解析 Maven 坐标为路径
func parseClientPath(maven string) (string, error) {
	parts := strings.Split(maven, ":")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid maven coordinate: %s", maven)
	}
	group := strings.ReplaceAll(parts[0], ".", "/")
	artifact := parts[1]
	version := parts[2]
	
	// 处理第4个部分（classifier 和 extension）
	classifier := ""
	ext := "jar"
	if len(parts) >= 4 {
		classifierAndExt := parts[3]
		if strings.Contains(classifierAndExt, "@") {
			tmp := strings.Split(classifierAndExt, "@")
			classifier = tmp[0]
			ext = tmp[1]
		} else {
			classifier = classifierAndExt
			// 如果没有 @，ext 保持默认的 "jar"
		}
	}
	
	// 构建文件名
	var fileName string
	if classifier != "" {
		fileName = fmt.Sprintf("%s-%s-%s.%s", artifact, version, classifier, ext)
	} else {
		fileName = fmt.Sprintf("%s-%s.%s", artifact, version, ext)
	}
	
	path := fmt.Sprintf("%s/%s/%s/%s", group, artifact, version, fileName)
	return path, nil
}

// 复制文件到目标目录
func copyClientFile(sourcePath, buildDir, destDir string) error {
	// 尝试多个可能的路径
	possiblePaths := []string{
		filepath.Join(buildDir, "libraries", sourcePath),  // 主要路径：libraries/下
		sourcePath,  // 直接路径
		filepath.Join(buildDir, sourcePath),  // buildDir/下
	}
	
	var actualPath string
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			actualPath = path
			break
		}
	}
	
	if actualPath == "" {
		return fmt.Errorf("source file not found in any of these locations: %v", possiblePaths)
	}
	
	destPath := filepath.Join(destDir, filepath.Base(actualPath))
	input, err := os.Open(actualPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer input.Close()
	output, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer output.Close()
	_, err = io.Copy(output, input)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	fmt.Printf("Copied %s to %s\n", actualPath, destPath)
	return nil
}

// 解析 install_profile.json 并复制 client 相关文件
func processInstallProfile(profilePath, buildDir, destDir string) error {
	fmt.Printf("Processing install_profile.json: %s\n", profilePath)
	
	// 列出 build 目录结构，帮助调试
	fmt.Printf("Build directory structure:\n")
	if err := listDirectory(buildDir, "", 2); err != nil {
		fmt.Printf("Warning: Failed to list build directory: %v\n", err)
	}
	
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return fmt.Errorf("Error reading install_profile.json: %v", err)
	}
	var profile map[string]interface{}
	if err := json.Unmarshal(data, &profile); err != nil {
		return fmt.Errorf("Error parsing install_profile.json: %v", err)
	}
	dataNode, ok := profile["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("data node not found or not an object")
	}
	fmt.Printf("Found %d data entries\n", len(dataNode))
	for key, value := range dataNode {
		if obj, ok := value.(map[string]interface{}); ok {
			if clientValue, exists := obj["client"]; exists {
				if clientStr, ok := clientValue.(string); ok {
					fmt.Printf("Processing client field in %s: %s\n", key, clientStr)
					if strings.HasPrefix(clientStr, "[") && strings.HasSuffix(clientStr, "]") {
						clientStr = strings.Trim(clientStr, "[]")
						fmt.Printf("Extracted maven coordinate: %s\n", clientStr)
						filePath, err := parseClientPath(clientStr)
						if err != nil {
							fmt.Printf("Warning: Failed to parse client path %s: %v\n", clientStr, err)
							continue
						}
						fmt.Printf("Parsed path: %s\n", filePath)
						// 直接使用解析出的相对路径，不添加 buildDir 前缀
						fmt.Printf("Looking for file in libraries: %s\n", filePath)
						if err := copyClientFile(filePath, buildDir, destDir); err != nil {
							fmt.Printf("Warning: Failed to copy client file %s: %v\n", filePath, err)
							continue
						}
					} else {
						fmt.Printf("Client field not in [maven] format: %s\n", clientStr)
					}
				}
			}
		}
	}
	return nil
}

// 列出目录结构的辅助函数
func listDirectory(dir, prefix string, maxDepth int) error {
	if maxDepth <= 0 {
		return nil
	}
	
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	
	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Printf("%s%s/\n", prefix, entry.Name())
			if err := listDirectory(filepath.Join(dir, entry.Name()), prefix+"  ", maxDepth-1); err != nil {
				return err
			}
		} else {
			fmt.Printf("%s%s\n", prefix, entry.Name())
		}
	}
	return nil
}

// BuildSpecificForgeClient 构建指定版本的 Forge 客户端
func BuildSpecificForgeClient(minecraftVersion string, forgeVersion string) (string, error) {
	fullVersion := fmt.Sprintf("%s-%s", minecraftVersion, forgeVersion)
	buildDir := filepath.Join("build", fullVersion)
	fmt.Printf("\nBuilding Forge client for Minecraft %s with Forge %s...\n", minecraftVersion, forgeVersion)

	// 创建 build/版本 目录
	err := os.MkdirAll(buildDir, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("Error creating build directory: %v", err)
	}

	fileName := fmt.Sprintf("forge-%s-installer.jar", fullVersion)
	installerPath := filepath.Join(buildDir, fileName)
	url := fmt.Sprintf("https://maven.minecraftforge.net/net/minecraftforge/forge/%s/%s", fullVersion, fileName)

	// 跳过已存在的 client.jar
	clientJarName := fmt.Sprintf("forge-%s-client.jar", fullVersion)
	clientJarPath := filepath.Join(fullVersion, clientJarName)
	if _, err := os.Stat(clientJarPath); err == nil {
		fmt.Printf("Already built: %s, skip.\n", clientJarPath)
		return clientJarPath, nil
	}

	fmt.Printf("Downloading %s to %s\n", fileName, installerPath)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("Error downloading installer: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Error downloading installer: received status code %d", resp.StatusCode)
	}

	outFile, err := os.Create(installerPath)
	if err != nil {
		return "", fmt.Errorf("Error creating file: %v", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return "", fmt.Errorf("Error saving installer: %v", err)
	}

	fmt.Printf("Downloaded installer to %s\n", installerPath)

	fmt.Println("Running Forge installer with --makeOffline...")
	cmd := exec.Command("java", "-jar", fileName, "--makeOffline")
	cmd.Dir = buildDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return "", fmt.Errorf("Error running installer: %v", err)
	}

	// 复制 client.jar
	fmt.Println("Copying client jar...")
	sourceFileName := fmt.Sprintf("forge-%s-client.jar", fullVersion)
	sourcePath := filepath.Join(buildDir, "libraries", "net", "minecraftforge", "forge", fullVersion, sourceFileName)

	// 兼容老版本（无 -client 后缀）
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		sourceFileName = fmt.Sprintf("forge-%s.jar", fullVersion)
		sourcePath = filepath.Join(buildDir, "libraries", "net", "minecraftforge", "forge", fullVersion, sourceFileName)
	}

	destDir := fullVersion
	if err := os.MkdirAll(destDir, os.ModePerm); err != nil {
		return "", fmt.Errorf("Error creating destination directory %s: %v", destDir, err)
	}
	destPath := filepath.Join(destDir, sourceFileName)

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("Error opening source file %s: %v", sourcePath, err)
	}
	defer sourceFile.Close()

	destFile, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("Error creating destination file %s: %v", destPath, err)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return "", fmt.Errorf("Error copying file from %s to %s: %v", sourcePath, destPath, err)
	}

	fmt.Printf("Successfully copied client jar to %s\n", destPath)

	// 解压 installer jar 里的 version.json 到 client jar 同目录
	err = extractVersionJson(installerPath, destDir)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	// 从 installer jar 提取 install_profile.json 到临时位置
	tempProfilePath := filepath.Join(buildDir, "temp_install_profile.json")
	err = extractInstallProfile(installerPath, tempProfilePath)
	if err != nil {
		fmt.Printf("Warning: Failed to extract install_profile.json: %v\n", err)
	} else {
		// 解析并复制 client 相关文件
		err = processInstallProfile(tempProfilePath, buildDir, destDir)
		if err != nil {
			fmt.Printf("Warning: %v\n", err)
		}
	}

	// 清理 build 目录
	os.RemoveAll(buildDir)

	return destPath, nil
}

func main() {
	latest := flag.Bool("latest", false, "只构建最新Forge版本")
	mc := flag.String("mc", "", "指定Minecraft版本, 例如 1.20.1")
	forge := flag.String("forge", "", "指定Forge版本, 例如 47.1.0")
	flag.Parse()

	// 拉取元数据
	resp, err := http.Get(metaURL)
	if err != nil {
		fmt.Printf("Error fetching metadata: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Error fetching metadata: status %d\n", resp.StatusCode)
		os.Exit(1)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("Error reading metadata: %v\n", err)
		os.Exit(1)
	}

	// 解析 Modrinth manifest.json
	var manifest struct {
		GameVersions []struct {
			ID      string `json:"id"`
			Loaders []struct {
				ID string `json:"id"`
			} `json:"loaders"`
		} `json:"gameVersions"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		fmt.Printf("Error parsing metadata: %v\n", err)
		os.Exit(1)
	}

	// 构建 meta map[string][]string
	meta := make(map[string][]string)
	for _, v := range manifest.GameVersions {
		if len(v.Loaders) == 0 {
			continue
		}
		mc := v.ID
		meta[mc] = []string{v.Loaders[0].ID} // 只取第一个 loader
	}

	// 新增：指定游戏版本和Forge版本
	if *mc != "" && *forge != "" {
		fmt.Printf("\n==== 构建指定版本 %s / %s ====\n", *mc, *forge)
		jarPath, err := BuildSpecificForgeClient(*mc, *forge)
		if err != nil {
			fmt.Printf("构建失败: %v\n", err)
			os.Exit(1)
		}
		f, _ := os.Create("artifacts.txt")
		fmt.Fprintf(f, "%s %s %s\n", jarPath, *mc, *forge)
		f.Close()
		fmt.Printf("构建完成: %s %s\n", *mc, *forge)
		return
	}

	if *mc != "" && *latest {
		// 指定MC版本的最新Forge
		forgeVersions, ok := meta[*mc]
		if !ok || len(forgeVersions) == 0 {
			fmt.Printf("未找到该Minecraft版本: %s\n", *mc)
			os.Exit(1)
		}
		sort.Strings(forgeVersions)
		latestForge := forgeVersions[len(forgeVersions)-1]
		fmt.Printf("\n==== 构建 %s / %s ====\n", *mc, latestForge)
		jarPath, err := BuildSpecificForgeClient(*mc, latestForge)
		if err != nil {
			fmt.Printf("构建失败: %v\n", err)
			os.Exit(1)
		}
		f, _ := os.Create("artifacts.txt")
		fmt.Fprintf(f, "%s %s %s\n", jarPath, *mc, latestForge)
		f.Close()
		fmt.Printf("构建完成: %s %s\n", *mc, latestForge)
		return
	}

	if *latest {
		// 所有MC的最新
		mcVersions := make([]*version.Version, 0, len(meta))
		mcVersionMap := make(map[string]string)
		for vStr := range meta {
			if !strings.Contains(vStr, ".") {
				continue
			}
			v, err := version.NewVersion(vStr)
			if err != nil {
				continue
			}
			mcVersions = append(mcVersions, v)
			mcVersionMap[v.Original()] = vStr
		}
		sort.Sort(version.Collection(mcVersions))
		latestMC := mcVersionMap[mcVersions[len(mcVersions)-1].Original()]
		forgeVersions := meta[latestMC]
		sort.Strings(forgeVersions)
		latestForge := forgeVersions[len(forgeVersions)-1]
		jarPath, err := BuildSpecificForgeClient(latestMC, latestForge)
		if err != nil {
			fmt.Printf("构建失败: %v\n", err)
			os.Exit(1)
		}
		f, _ := os.Create("artifacts.txt")
		fmt.Fprintf(f, "%s %s %s\n", jarPath, latestMC, latestForge)
		f.Close()
		fmt.Printf("最新版本构建完成: %s %s\n", latestMC, latestForge)
		return
	}

	fmt.Println("请使用以下参数之一:")
	fmt.Println("  --latest                          构建最新Minecraft版本的最新Forge")
	fmt.Println("  --mc <version> --latest          构建指定Minecraft版本的最新Forge")
	fmt.Println("  --mc <version> --forge <version> 构建指定Minecraft版本和Forge版本")
	os.Exit(1)
} 