package tests

import (
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"

	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1beta1"
	"kubevirt.io/containerized-data-importer/pkg/common"
	"kubevirt.io/containerized-data-importer/pkg/controller"
	"kubevirt.io/containerized-data-importer/tests/framework"
	"kubevirt.io/containerized-data-importer/tests/utils"
)

var _ = Describe("Transport Tests", func() {

	const (
		secretPrefix            = "transport-e2e-sec"
		targetFile              = "tinyCore.iso"
		targetQCOWFile          = "tinyCore.qcow2"
		targetQCOWImage         = "tinycoreqcow2"
		targetRawImage          = "tinycoreqcow2"
		targetArchivedImage     = "tinycoreisotar"
		targetArchivedImageHash = "b354a50183e70ee2ed3413eea67fe153"
		targetNodePullImage     = "cdi-func-test-tinycore"
	)

	var (
		ns  string
		f   = framework.NewFramework("transport")
		sec *v1.Secret
	)

	BeforeEach(func() {
		ns = f.Namespace.Name
		By(fmt.Sprintf("Waiting for all \"%s/%s\" deployment replicas to be Ready", f.CdiInstallNs, utils.FileHostName))
		utils.WaitForDeploymentReplicasReadyOrDie(f.K8sClient, f.CdiInstallNs, utils.FileHostName)
	})

	// it() is the body of the test and is executed once per Entry() by DescribeTable()
	// closes over c and ns
	it := func(ep func() string, file, expectedHash, accessKey, secretKey, source, certConfigMap, registryImportMethod string, insecureRegistry, shouldSucceed bool) {

		var (
			err error // prevent shadowing
		)

		var endpoint string
		if registryImportMethod == string(cdiv1.RegistryPullNode) {
			endpoint = ep() + "/" + file + ":" + f.DockerTag
		} else {
			endpoint = ep() + "/" + file
		}

		pvcAnn := map[string]string{
			controller.AnnEndpoint:             endpoint,
			controller.AnnSecret:               "",
			controller.AnnSource:               source,
			controller.AnnRegistryImportMethod: registryImportMethod,
		}

		if accessKey != "" || secretKey != "" {
			By(fmt.Sprintf("Creating secret for endpoint %s", ep()))
			if accessKey == "" {
				accessKey = utils.AccessKeyValue
			}
			if secretKey == "" {
				secretKey = utils.SecretKeyValue
			}
			stringData := make(map[string]string)
			stringData[common.KeyAccess] = accessKey
			stringData[common.KeySecret] = secretKey

			sec, err = utils.CreateSecretFromDefinition(f.K8sClient, utils.NewSecretDefinition(nil, stringData, nil, ns, secretPrefix))
			Expect(err).NotTo(HaveOccurred(), "Error creating test secret")
			pvcAnn[controller.AnnSecret] = sec.Name
		}

		if certConfigMap != "" {
			n, err := utils.CopyConfigMap(f.K8sClient, f.CdiInstallNs, certConfigMap, ns, "")
			Expect(err).To(BeNil())
			pvcAnn[controller.AnnCertConfigMap] = n
		}

		if insecureRegistry {
			err = utils.AddInsecureRegistry(f.CrClient, ep())
			Expect(err).To(BeNil())

			hasInsecReg, err := utils.HasInsecureRegistry(f.CrClient, ep())
			Expect(err).ToNot(HaveOccurred())
			Expect(hasInsecReg).To(BeTrue())

			defer utils.RemoveInsecureRegistry(f.CrClient, ep())
		}

		By(fmt.Sprintf("Creating PVC with endpoint annotation %q", pvcAnn[controller.AnnEndpoint]))
		pvc, err := utils.CreatePVCFromDefinition(f.K8sClient, ns, utils.NewPVCDefinition("transport-e2e", "400Mi", pvcAnn, nil))
		Expect(err).NotTo(HaveOccurred(), "Error creating PVC")

		By("Verifying pvc was created")
		pvc, err = utils.WaitForPVC(f.K8sClient, pvc.Namespace, pvc.Name)
		Expect(err).ToNot(HaveOccurred())
		f.ForceBindIfWaitForFirstConsumer(pvc)

		if shouldSucceed {
			By("Verify PVC status annotation says succeeded")
			found, err := utils.WaitPVCPodStatusSucceeded(f.K8sClient, pvc)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			By("Verifying PVC is not empty")
			Expect(framework.VerifyPVCIsEmpty(f, pvc, "")).To(BeFalse(), fmt.Sprintf("Found 0 imported files on PVC %q", pvc.Namespace+"/"+pvc.Name))

			switch pvcAnn[controller.AnnSource] {
			case controller.SourceHTTP, controller.SourceRegistry:
				if file != targetFile {
					By("Verify content")
					same, err := f.VerifyTargetPVCContentMD5(f.Namespace, pvc, utils.DefaultImagePath, expectedHash, utils.UploadFileSize)
					Expect(err).ToNot(HaveOccurred())
					Expect(same).To(BeTrue())
				}
			}
			By("Verifying the image is sparse")
			Expect(f.VerifySparse(f.Namespace, pvc)).To(BeTrue())
		} else {
			By("Verify PVC status annotation says failed")
			found, err := utils.WaitPVCPodStatusRunning(f.K8sClient, pvc)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Eventually(func() bool {
				importer, err := utils.FindPodByPrefix(f.K8sClient, ns, common.ImporterPodName, common.CDILabelSelector)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Unable to get importer pod %q", ns+"/"+common.ImporterPodName))
				return importer.Status.ContainerStatuses[0].RestartCount > 0
			}, timeout, pollingInterval).Should(BeTrue())
		}
	}
	httpNoAuthEp := func() string {
		return fmt.Sprintf("http://%s:%d", utils.FileHostName+"."+f.CdiInstallNs, utils.HTTPNoAuthPort)
	}
	httpsNoAuthEp := func() string {
		return fmt.Sprintf("https://%s:%d", utils.FileHostName+"."+f.CdiInstallNs, utils.HTTPSNoAuthPort)
	}
	httpAuthEp := func() string {
		return fmt.Sprintf("http://%s:%d", utils.FileHostName+"."+f.CdiInstallNs, utils.HTTPAuthPort)
	}
	registryNoAuthEp := func() string { return fmt.Sprintf("docker://%s", utils.RegistryHostName+"."+f.CdiInstallNs) }
	registryAuthEp := func() string { return fmt.Sprintf("docker://%s.%s:%d", utils.RegistryHostName, f.CdiInstallNs, 1443) }
	altRegistryNoAuthEp := func() string { return fmt.Sprintf("docker://%s.%s:%d", utils.RegistryHostName, f.CdiInstallNs, 5000) }
	trustedRegistryEp := func() string { return fmt.Sprintf("docker://%s", f.DockerPrefix) }

	DescribeTable("Transport Test Table", it,
		Entry("[test_id:5059]should connect to http endpoint without credentials", httpNoAuthEp, targetFile, "", "", "", controller.SourceHTTP, "", "", false, true),
		Entry("[test_id:5060]should connect to http endpoint with credentials", httpAuthEp, targetFile, "", utils.AccessKeyValue, utils.SecretKeyValue, controller.SourceHTTP, "", "", false, true),
		Entry("[test_id:5061]should not connect to http endpoint with invalid credentials", httpAuthEp, targetFile, "", "invalid", "invalid", controller.SourceHTTP, "", "", false, false),
		Entry("[test_id:5062]should connect to QCOW http endpoint without credentials", httpNoAuthEp, targetQCOWFile, utils.UploadFileMD5, "", "", controller.SourceHTTP, "", "", false, true),
		Entry("[test_id:5063]should connect to QCOW http endpoint with credentials", httpAuthEp, targetQCOWFile, utils.UploadFileMD5, utils.AccessKeyValue, utils.SecretKeyValue, controller.SourceHTTP, "", "", false, true),
		Entry("[test_id:5064]should succeed to import from registry when image contains valid qcow file, custom cert", registryNoAuthEp, targetQCOWImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "cdi-docker-registry-host-certs", "", false, true),
		Entry("[test_id:5065]should fail to import from registry when image contains valid qcow file, custom cert+auth, invalid credentials", registryAuthEp, targetQCOWImage, utils.UploadFileMD5, "invalid", "invalid", controller.SourceRegistry, "cdi-docker-registry-host-certs", "", true, false),
		Entry("[test_id:5066]should succeed to import from registry when image contains valid qcow file, custom cert+auth, valid credentials", registryAuthEp, targetQCOWImage, utils.UploadFileMD5, utils.AccessKeyValue, utils.SecretKeyValue, controller.SourceRegistry, "cdi-docker-registry-host-certs", "", true, true),
		Entry("[test_id:5067]should succeed to import from registry when image contains valid qcow file, no auth", registryNoAuthEp, targetQCOWImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "", "", true, true),
		Entry("[test_id:5068]should succeed to import from registry when image contains valid qcow file, auth", altRegistryNoAuthEp, targetQCOWImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "", "", true, true),
		Entry("[test_id:5069]should fail no certs", registryNoAuthEp, targetQCOWImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "", "", false, false),
		Entry("[test_id:5070]should fail bad certs", registryNoAuthEp, targetQCOWImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "cdi-file-host-certs", "", false, false),
		Entry("[test_id:5071]should succeed to import from registry when image contains valid raw file", registryNoAuthEp, targetRawImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "cdi-docker-registry-host-certs", "", false, true),
		Entry("[test_id:5072]should succeed to import from registry when image contains valid archived raw file", registryNoAuthEp, targetArchivedImage, targetArchivedImageHash, "", "", controller.SourceRegistry, "cdi-docker-registry-host-certs", "", false, true),
		Entry("[test_id:5073]should not connect to https endpoint without cert", httpsNoAuthEp, targetFile, "", "", "", controller.SourceHTTP, "", "", false, false),
		Entry("[test_id:5074]should connect to https endpoint with cert", httpsNoAuthEp, targetFile, "", "", "", controller.SourceHTTP, "cdi-file-host-certs", "", false, true),
		Entry("[test_id:5075]should not connect to https endpoint with bad cert", httpsNoAuthEp, targetFile, "", "", "", controller.SourceHTTP, "cdi-docker-registry-host-certs", "", false, false),
		Entry("[test_id:7240]should succeed to node pull import from registry when image contains valid iso file, no auth", trustedRegistryEp, targetNodePullImage, utils.UploadFileMD5, "", "", controller.SourceRegistry, "", string(cdiv1.RegistryPullNode), false, true),
	)
})
