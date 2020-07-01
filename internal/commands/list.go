package commands

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	promv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerImage ...
type ContainerImage struct {
	Host       string
	Name       string
	Repository string
	Version    string
}

func (d ContainerImage) String() string {
	var output string
	if d.Host != "" {
		output = d.Host + "/"
	}

	output += d.Repository + ":" + d.Version

	return output
}

func newListCommand() *cobra.Command {
	cmd := cobra.Command{
		Use:   "list",
		Short: "List the images found in the repository",
		Args:  cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := viper.BindPFlag("output", cmd.Flags().Lookup("output")); err != nil {
				return fmt.Errorf("bind flag: %w", err)
			}

			if err := viper.BindPFlag("mirror", cmd.Flags().Lookup("mirror")); err != nil {
				return fmt.Errorf("bind flag: %w", err)
			}

			if err := runListCommand(args); err != nil {
				return fmt.Errorf("list: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringP("output", "o", "", "output path for the image list")
	cmd.Flags().StringP("mirror", "m", "", "mirror prefix")

	return &cmd
}

func runListCommand(args []string) error {
	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working dir: %w", err)
	}

	listPath := filepath.Join(workingDir, args[0])

	var images []ContainerImage
	if filepath.Ext(listPath) == ".txt" && viper.GetString("mirror") != "" {
		originalImages, err := GetImagesFromFile(listPath)
		if err != nil {
			return fmt.Errorf("get images from path: %w", err)
		}

		for _, originalImage := range originalImages {
			image := getOriginalImage(originalImage, viper.GetString("mirror"))
			images = append(images, image)
		}
	} else {
		images, err = GetImagesFromYaml(listPath)
		if err != nil {
			return fmt.Errorf("get images from path: %w", err)
		}
	}

	if viper.GetString("output") != "" {
		outputFile := filepath.Join(workingDir, viper.GetString("output"))
		if err := writeListToFile(images, outputFile); err != nil {
			return fmt.Errorf("writing list to file: %w", err)
		}
	} else {
		for _, image := range images {
			fmt.Println(image)
		}
	}

	return nil
}

// GetImagesFromFile returns a collection of images from a text file
func GetImagesFromFile(filePath string) ([]ContainerImage, error) {
	if filepath.Ext(filePath) != ".txt" {
		return nil, fmt.Errorf("expected .txt file")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var images []string
	for scanner.Scan() {
		images = append(images, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning file: %w", err)
	}

	marshaledImages := marshalImages(images)

	return marshaledImages, nil
}

// GetImagesFromYaml finds all yaml files in a given path and returns
// all of the images found in the manifests
func GetImagesFromYaml(path string) ([]ContainerImage, error) {
	files, err := getYamlFiles(path)
	if err != nil {
		return nil, fmt.Errorf("get yaml files: %w", err)
	}

	yamlFiles, err := splitYamlFiles(files)
	if err != nil {
		return nil, fmt.Errorf("split yaml files: %w", err)
	}

	var imageList []string
	for _, yamlFile := range yamlFiles {
		var typeMeta metav1.TypeMeta
		if err := yaml.Unmarshal(yamlFile, &typeMeta); err != nil {
			continue
		}

		if typeMeta.Kind == "Prometheus" {
			var prometheus promv1.Prometheus
			if err := yaml.Unmarshal(yamlFile, &prometheus); err != nil {
				return nil, fmt.Errorf("unmarshal prometheus: %w", err)
			}

			var prometheusImage string
			if prometheus.Spec.BaseImage != "" {
				prometheusImage = prometheus.Spec.BaseImage + ":" + prometheus.Spec.Version
			} else {
				prometheusImage = *prometheus.Spec.Image
			}

			if len(prometheus.Spec.Containers) > 0 {
				imageList = append(imageList, getImagesFromContainers(prometheus.Spec.Containers)...)
			}

			if len(prometheus.Spec.InitContainers) > 0 {
				imageList = append(imageList, getImagesFromContainers(prometheus.Spec.InitContainers)...)
			}

			imageList = append(imageList, prometheusImage)
			continue
		}

		if typeMeta.Kind == "Alertmanager" {
			var alertmanager promv1.Alertmanager
			if err := yaml.Unmarshal(yamlFile, &alertmanager); err != nil {
				return nil, fmt.Errorf("unmarshal alertmanager: %w", err)
			}

			var alertmanagerImage string
			if alertmanager.Spec.BaseImage != "" {
				alertmanagerImage = alertmanager.Spec.BaseImage + ":" + alertmanager.Spec.Version
			} else {
				alertmanagerImage = *alertmanager.Spec.Image
			}

			if len(alertmanager.Spec.Containers) > 0 {
				imageList = append(imageList, getImagesFromContainers(alertmanager.Spec.Containers)...)
			}

			if len(alertmanager.Spec.InitContainers) > 0 {
				imageList = append(imageList, getImagesFromContainers(alertmanager.Spec.InitContainers)...)
			}

			imageList = append(imageList, alertmanagerImage)
			continue
		}

		type BaseSpec struct {
			Template corev1.PodTemplateSpec `json:"template" protobuf:"bytes,3,opt,name=template"`
		}

		type BaseType struct {
			Spec BaseSpec `json:"spec,omitempty" protobuf:"bytes,2,opt,name=spec"`
		}

		var contents BaseType
		if err := yaml.Unmarshal(yamlFile, &contents); err != nil {
			continue
		}

		imageList = append(imageList, getImagesFromContainers(contents.Spec.Template.Spec.InitContainers)...)
		imageList = append(imageList, getImagesFromContainers(contents.Spec.Template.Spec.Containers)...)
	}

	dedupedImageList := dedupeImages(imageList)
	marshaledImages := marshalImages(dedupedImageList)

	return marshaledImages, nil
}

func getOriginalImage(image ContainerImage, mirrorPrefix string) ContainerImage {
	quayMappings := []string{
		"kubernetes-ingress-controller",
		"coreos",
	}

	originalHost := "docker.io"
	for _, quayMapping := range quayMappings {
		if strings.Contains(image.Repository, quayMapping) {
			originalHost = "quay.io"
		}
	}

	var originalRepository string
	if strings.Contains(mirrorPrefix, "/") {
		mirrorRepository := strings.SplitN(mirrorPrefix, "/", 2)[1]
		originalRepository = strings.Replace(image.Repository, mirrorRepository+"/", "", 1)
	}

	originalImage := ContainerImage{
		Host:       originalHost,
		Repository: originalRepository,
		Name:       image.Name,
		Version:    image.Version,
	}

	return originalImage
}

func marshalImages(images []string) []ContainerImage {
	var containerImages []ContainerImage
	for _, image := range images {
		imageTokens := strings.Split(image, ":")
		imagePaths := strings.Split(imageTokens[0], "/")
		imageName := imagePaths[len(imagePaths)-1]

		var imageHost string
		var imageRepository string
		if strings.Contains(imagePaths[0], ".io") {
			imageHost = imagePaths[0]
		} else {
			imageHost = ""
		}

		if imageHost != "" {
			imageRepository = strings.TrimPrefix(imageTokens[0], imageHost+"/")
		} else {
			imageRepository = imageTokens[0]
		}

		containerImage := ContainerImage{
			Host:       imageHost,
			Repository: imageRepository,
			Name:       imageName,
			Version:    imageTokens[1],
		}

		containerImages = append(containerImages, containerImage)
	}

	return containerImages
}

func writeListToFile(images []ContainerImage, outputFile string) error {
	f, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	for _, value := range images {
		if _, err := fmt.Fprintln(f, value); err != nil {
			return fmt.Errorf("writing image to file: %w", err)
		}
	}

	return nil
}

func getImagesFromContainers(containers []corev1.Container) []string {
	var images []string
	for _, container := range containers {
		images = append(images, container.Image)

		for _, arg := range container.Args {
			if !strings.Contains(arg, ":") || strings.Contains(arg, "=:") {
				continue
			}

			argTokens := strings.Split(arg, "=")
			images = append(images, argTokens[1])
		}
	}

	return images
}

func getYamlFiles(path string) ([]string, error) {
	var files []string
	err := filepath.Walk(path, func(currentFilePath string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk path: %w", err)
		}

		if fileInfo.IsDir() && fileInfo.Name() == ".git" {
			return filepath.SkipDir
		}

		if fileInfo.IsDir() {
			return nil
		}

		if filepath.Ext(currentFilePath) != ".yaml" && filepath.Ext(currentFilePath) != ".yml" {
			return nil
		}

		files = append(files, currentFilePath)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return files, nil
}

func splitYamlFiles(files []string) ([][]byte, error) {
	var yamlFiles [][]byte
	for _, file := range files {
		fileContent, err := ioutil.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("open file: %w", err)
		}

		individualYamlFiles := doSplit(fileContent)

		yamlFiles = append(yamlFiles, individualYamlFiles...)
	}

	return yamlFiles, nil
}

func contains(images []string, image string) bool {
	for _, currentImage := range images {
		if strings.EqualFold(currentImage, image) {
			return true
		}
	}

	return false
}

func dedupeImages(images []string) []string {
	var dedupedImageList []string
	for _, image := range images {
		if !contains(dedupedImageList, image) {
			dedupedImageList = append(dedupedImageList, image)
		}
	}

	return dedupedImageList
}

func doSplit(data []byte) [][]byte {
	linebreak := "\n"
	windowsLineEnding := bytes.Contains(data, []byte("\r\n"))
	if windowsLineEnding && runtime.GOOS == "windows" {
		linebreak = "\r\n"
	}

	return bytes.Split(data, []byte(linebreak+"---"+linebreak))
}