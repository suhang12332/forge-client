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

const metaURL = "https://files.minecraftforge.net/net/minecraftforge/forge/maven-metadata.json"

// BuildForgeClient 构建指定版本的 Forge 客户端，并返回 client.jar 路径
func BuildForgeClient(minecraftVersion string, forgeVersion string) (string, error) {
	fullVersion := forgeVersion
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
	err = extractVersionJson(installerPath, destDir)
	if err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	// 清理 build 目录
	os.RemoveAll(buildDir)

	return destPath, nil
}

func main() {
	latest := flag.Bool("latest", false, "只构建最新Forge版本")
	mc := flag.String("mc", "", "指定Minecraft版本, 例如 1.20.1")
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

	var meta map[string][]string
	if err := json.Unmarshal(body, &meta); err != nil {
		fmt.Printf("Error parsing metadata: %v\n", err)
		os.Exit(1)
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
		jarPath, err := BuildForgeClient(*mc, latestForge)
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
		jarPath, err := BuildForgeClient(latestMC, latestForge)
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

	fmt.Println("请使用 --latest 或 --mc <version> --latest 参数")
	os.Exit(1)
} 