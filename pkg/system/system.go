package system

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/noobaa/noobaa-operator/build/_output/bundle"
	nbv1 "github.com/noobaa/noobaa-operator/pkg/apis/noobaa/v1alpha1"
	"github.com/noobaa/noobaa-operator/pkg/nb"
	"github.com/noobaa/noobaa-operator/pkg/util"

	dockerref "github.com/docker/distribution/reference"
	semver "github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	conditionsv1 "github.com/openshift/custom-resource-status/conditions/v1"

)

const (
	// ContainerImageOrg is the org of the default image url
	ContainerImageOrg = "noobaa"
	// ContainerImageRepo is the repo of the default image url
	ContainerImageRepo = "noobaa-core"
	// ContainerImageTag is the tag of the default image url
	ContainerImageTag = "5"
	// ContainerImageConstraintSemver is the contraints of supported image versions
	ContainerImageConstraintSemver = ">=5, <6"
	// ContainerImageName is the default image name without the tag/version
	ContainerImageName = ContainerImageOrg + "/" + ContainerImageRepo
	// ContainerImage is the full default image url
	ContainerImage = ContainerImageName + ":" + ContainerImageTag
	// MongoImage is the default mongodb image url
	MongoImage = "centos/mongodb-36-centos7"

	// AdminAccountEmail is the default email used for admin account
	AdminAccountEmail = "admin@noobaa.io"
)

var (
	// ContainerImageConstraint is the instantiated semver contraints used for image verification
	ContainerImageConstraint, _ = semver.NewConstraint(ContainerImageConstraintSemver)

	// NooBaaType is and empty noobaa struct used for passing the object type
	NooBaaType = &nbv1.NooBaa{}
)

// System is the context for loading or reconciling a noobaa system
type System struct {
	Request  types.NamespacedName
	Client   client.Client
	Scheme   *runtime.Scheme
	Ctx      context.Context
	Logger   *logrus.Entry
	Recorder record.EventRecorder
	NBClient nb.Client

	NooBaa       *nbv1.NooBaa
	CoreApp      *appsv1.StatefulSet
	ServiceMgmt  *corev1.Service
	ServiceS3    *corev1.Service
	SecretServer *corev1.Secret
	SecretOp     *corev1.Secret
	SecretAdmin  *corev1.Secret
}

// New initializes a system to be used for loading or reconciling a noobaa system
func New(req types.NamespacedName, client client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *System {
	s := &System{
		Request:      req,
		Client:       client,
		Scheme:       scheme,
		Recorder:     recorder,
		Ctx:          context.TODO(),
		Logger:       logrus.WithFields(logrus.Fields{"ns": req.Namespace, "sys": req.Name}),
		NooBaa:       util.KubeObject(bundle.File_deploy_crds_noobaa_v1alpha1_noobaa_cr_yaml).(*nbv1.NooBaa),
		CoreApp:      util.KubeObject(bundle.File_deploy_internal_statefulset_core_yaml).(*appsv1.StatefulSet),
		ServiceMgmt:  util.KubeObject(bundle.File_deploy_internal_service_mgmt_yaml).(*corev1.Service),
		ServiceS3:    util.KubeObject(bundle.File_deploy_internal_service_s3_yaml).(*corev1.Service),
		SecretServer: util.KubeObject(bundle.File_deploy_internal_secret_server_yaml).(*corev1.Secret),
		SecretOp:     util.KubeObject(bundle.File_deploy_internal_secret_operator_yaml).(*corev1.Secret),
		SecretAdmin:  util.KubeObject(bundle.File_deploy_internal_secret_admin_yaml).(*corev1.Secret),
	}
	SecretResetStringDataFromData(s.SecretOp)
	SecretResetStringDataFromData(s.SecretAdmin)

	// Set Namespace
	s.NooBaa.Namespace = s.Request.Namespace
	s.CoreApp.Namespace = s.Request.Namespace
	s.ServiceMgmt.Namespace = s.Request.Namespace
	s.ServiceS3.Namespace = s.Request.Namespace
	s.SecretServer.Namespace = s.Request.Namespace
	s.SecretOp.Namespace = s.Request.Namespace
	s.SecretAdmin.Namespace = s.Request.Namespace

	// Set Names
	s.NooBaa.Name = s.Request.Name
	s.CoreApp.Name = s.Request.Name + "-core"
	s.ServiceMgmt.Name = s.Request.Name + "-mgmt"
	s.ServiceS3.Name = "s3" // TODO: handle collision in namespace
	s.SecretServer.Name = s.Request.Name + "-server"
	s.SecretOp.Name = s.Request.Name + "-operator"
	s.SecretAdmin.Name = s.Request.Name + "-admin"

	return s
}

// Load reads the state of the kubernetes objects of the system
func (s *System) Load() {
	util.KubeCheck(s.Client, s.NooBaa)
	util.KubeCheck(s.Client, s.CoreApp)
	util.KubeCheck(s.Client, s.ServiceMgmt)
	util.KubeCheck(s.Client, s.ServiceS3)
	util.KubeCheck(s.Client, s.SecretServer)
	util.KubeCheck(s.Client, s.SecretOp)
	util.KubeCheck(s.Client, s.SecretAdmin)
	SecretResetStringDataFromData(s.SecretOp)
	SecretResetStringDataFromData(s.SecretAdmin)
}

// Reconcile reads that state of the cluster for a System object,
// and makes changes based on the state read and what is in the System.Spec.
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (s *System) Reconcile() (reconcile.Result, error) {

	log := s.Logger.WithField("func", "Reconcile")
	log.Infof("Start ...")

	util.KubeCheck(s.Client, s.NooBaa)
	if s.NooBaa.UID == "" {
		log.Infof("NooBaa not found or already deleted. Skip reconcile.")
		return reconcile.Result{}, nil
	}

	err := CombineErrors(
		s.ReconcileSystem(),
		s.UpdateSystemStatus(),
	)
	if err == nil {
		log.Infof("✅ Done")
		return reconcile.Result{}, nil
	}
	if !IsPersistentError(err) {
		log.Warnf("⏳ Temporary Error: %s", err)
		return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
	}
	log.Errorf("❌ Persistent Error: %s", err)
	return reconcile.Result{}, nil
}

// ReconcileSystem runs the reconcile flow and populates System.Status.
func (s *System) ReconcileSystem() error {

	s.SetPhase(nbv1.SystemPhaseVerifying)

	if err := s.CheckSpecImage(); err != nil {
		return err
	}

	s.SetPhase(nbv1.SystemPhaseCreating)

	if err := s.ReconcileSecretServer(); err != nil {
		s.setErrorCondition(err)
		return err
	}
	if err := s.ReconcileObject(s.CoreApp, s.SetDesiredCoreApp); err != nil {
		s.setErrorCondition(err)
		return err
	}
	if err := s.ReconcileObject(s.ServiceMgmt, s.SetDesiredServiceMgmt); err != nil {
		s.setErrorCondition(err)
		return err
	}
	if err := s.ReconcileObject(s.ServiceS3, s.SetDesiredServiceS3); err != nil {
		s.setErrorCondition(err)
		return err
	}

	s.CheckServiceStatus(s.ServiceMgmt, &s.NooBaa.Status.Services.ServiceMgmt, "mgmt-https")
	s.CheckServiceStatus(s.ServiceS3, &s.NooBaa.Status.Services.ServiceS3, "s3-https")

	s.SetPhase(nbv1.SystemPhaseWaitingToConnect)

	if err := s.InitNooBaaClient(); err != nil {
		s.setErrorCondition(err)
		return err
	}

	s.SetPhase(nbv1.SystemPhaseConfiguring)

	if err := s.ReconcileSecretOp(); err != nil {
		s.setErrorCondition(err)
		return err
	}

	if err := s.ReconcileSecretAdmin(); err != nil {
		s.setErrorCondition(err)
		return err
	}

	s.SetPhase(nbv1.SystemPhaseReady)

	return s.Complete()
}

// ReconcileSecretServer creates a secret needed for the server pod
func (s *System) ReconcileSecretServer() error {
	util.KubeCheck(s.Client, s.SecretServer)
	SecretResetStringDataFromData(s.SecretServer)

	if s.SecretServer.StringData["jwt"] == "" {
		s.SecretServer.StringData["jwt"] = randomBase64(16)
	}
	if s.SecretServer.StringData["server_secret"] == "" {
		s.SecretServer.StringData["server_secret"] = randomHex(4)
	}
	s.Own(s.SecretServer)
	util.KubeCreateSkipExisting(s.Client, s.SecretServer)
	return nil
}

// SetDesiredCoreApp updates the CoreApp as desired for reconciling
func (s *System) SetDesiredCoreApp() {
	s.CoreApp.Spec.Template.Labels["noobaa-core"] = s.Request.Name
	s.CoreApp.Spec.Template.Labels["noobaa-mgmt"] = s.Request.Name
	s.CoreApp.Spec.Template.Labels["noobaa-s3"] = s.Request.Name
	s.CoreApp.Spec.Selector.MatchLabels["noobaa-core"] = s.Request.Name
	s.CoreApp.Spec.ServiceName = s.ServiceMgmt.Name

	podSpec := &s.CoreApp.Spec.Template.Spec
	podSpec.ServiceAccountName = "noobaa-operator" // TODO do we use the same SA?
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Image == "NOOBAA_IMAGE" {
			podSpec.InitContainers[i].Image = s.NooBaa.Status.ActualImage
		}
	}
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Image == "NOOBAA_IMAGE" {
			podSpec.Containers[i].Image = s.NooBaa.Status.ActualImage
		} else if podSpec.Containers[i].Image == "MONGO_IMAGE" {
			if s.NooBaa.Spec.MongoImage == nil {
				podSpec.Containers[i].Image = MongoImage
			} else {
				podSpec.Containers[i].Image = *s.NooBaa.Spec.MongoImage
			}
		}
	}
	if s.NooBaa.Spec.ImagePullSecret == nil {
		podSpec.ImagePullSecrets =
			[]corev1.LocalObjectReference{}
	} else {
		podSpec.ImagePullSecrets =
			[]corev1.LocalObjectReference{*s.NooBaa.Spec.ImagePullSecret}
	}
	for i := range s.CoreApp.Spec.VolumeClaimTemplates {
		pvc := &s.CoreApp.Spec.VolumeClaimTemplates[i]
		pvc.Spec.StorageClassName = s.NooBaa.Spec.StorageClassName

		// TODO we want to own the PVC's by NooBaa system but get errors on openshift:
		//   Warning  FailedCreate  56s  statefulset-controller
		//   create Pod noobaa-core-0 in StatefulSet noobaa-core failed error:
		//   Failed to create PVC mongo-datadir-noobaa-core-0:
		//   persistentvolumeclaims "mongo-datadir-noobaa-core-0" is forbidden:
		//   cannot set blockOwnerDeletion if an ownerReference refers to a resource
		//   you can't set finalizers on: , <nil>, ...

		// s.Own(pvc)
	}
}

// SetDesiredServiceMgmt updates the ServiceMgmt as desired for reconciling
func (s *System) SetDesiredServiceMgmt() {
	s.ServiceMgmt.Spec.Selector["noobaa-mgmt"] = s.Request.Name
}

// SetDesiredServiceS3 updates the ServiceS3 as desired for reconciling
func (s *System) SetDesiredServiceS3() {
	s.ServiceS3.Spec.Selector["noobaa-s3"] = s.Request.Name
}

// CheckSpecImage checks the System.Spec.Image property,
// and sets System.Status.ActualImage
func (s *System) CheckSpecImage() error {

	log := s.Logger.WithField("func", "CheckSpecImage")

	specImage := ContainerImage
	if s.NooBaa.Spec.Image != nil {
		specImage = *s.NooBaa.Spec.Image
	}

	// Parse the image spec as a docker image url
	imageRef, err := dockerref.Parse(specImage)

	// If the image cannot be parsed log the incident and mark as persistent error
	// since we don't need to retry until the spec is updated.
	if err != nil {
		log.Errorf("Invalid image %s: %s", specImage, err)
		if s.Recorder != nil {
			s.Recorder.Eventf(s.NooBaa, corev1.EventTypeWarning,
				"BadImage", `Invalid image requested "%s"`, specImage)
		}
		s.SetPhase(nbv1.SystemPhaseRejected)
		return NewPersistentError(err)
	}

	// Get the image name and tag
	imageName := ""
	imageTag := ""
	switch image := imageRef.(type) {
	case dockerref.NamedTagged:
		log.Infof("Parsed image (NamedTagged) %v", image)
		imageName = image.Name()
		imageTag = image.Tag()
	case dockerref.Tagged:
		log.Infof("Parsed image (Tagged) %v", image)
		imageTag = image.Tag()
	case dockerref.Named:
		log.Infof("Parsed image (Named) %v", image)
		imageName = image.Name()
	default:
		log.Infof("Parsed image (unstructured) %v", image)
	}

	if imageName == ContainerImageName {
		version, err := semver.NewVersion(imageTag)
		if err == nil {
			log.Infof("Parsed version \"%s\" from image tag \"%s\"", version.String(), imageTag)
			if !ContainerImageConstraint.Check(version) {
				log.Errorf("Unsupported image version \"%s\" for contraints \"%s\"",
					imageRef.String(), ContainerImageConstraint.String())
				if s.Recorder != nil {
					s.Recorder.Eventf(s.NooBaa, corev1.EventTypeWarning,
						"BadImage", `Unsupported image version requested "%s" not matching constraints "%s"`,
						imageRef, ContainerImageConstraint)
				}
				s.SetPhase(nbv1.SystemPhaseRejected)
				return NewPersistentError(fmt.Errorf(`Unsupported image version "%+v"`, imageRef))
			}
		} else {
			log.Infof("Using custom image \"%s\" contraints \"%s\"", imageRef.String(), ContainerImageConstraint.String())
			if s.Recorder != nil {
				s.Recorder.Eventf(s.NooBaa, corev1.EventTypeNormal,
					"CustomImage", `Custom image version requested "%s", I hope you know what you're doing ...`, imageRef)
			}
		}
	} else {
		log.Infof("Using custom image name \"%s\" the default is \"%s\"", imageRef.String(), ContainerImageName)
		if s.Recorder != nil {
			s.Recorder.Eventf(s.NooBaa, corev1.EventTypeNormal,
				"CustomImage", `Custom image requested "%s", I hope you know what you're doing ...`, imageRef)
		}
	}

	// Set ActualImage to be updated in the noobaa status
	s.NooBaa.Status.ActualImage = specImage
	return nil
}

// CheckServiceStatus populates the status of a service by detecting all of its addresses
func (s *System) CheckServiceStatus(srv *corev1.Service, status *nbv1.ServiceStatus, portName string) {

	log := s.Logger.WithField("func", "CheckServiceStatus").WithField("service", srv.Name)
	*status = nbv1.ServiceStatus{}
	servicePort := nb.FindPortByName(srv, portName)
	proto := "http"
	if strings.HasSuffix(portName, "https") {
		proto = "https"
	}

	// Node IP:Port
	// Pod IP:Port
	pods := corev1.PodList{}
	podsListOptions := &client.ListOptions{
		Namespace:     s.Request.Namespace,
		LabelSelector: labels.SelectorFromSet(srv.Spec.Selector),
	}
	err := s.Client.List(s.Ctx, podsListOptions, &pods)
	if err == nil {
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				if pod.Status.HostIP != "" {
					status.NodePorts = append(
						status.NodePorts,
						fmt.Sprintf("%s://%s:%d", proto, pod.Status.HostIP, servicePort.NodePort),
					)
				}
				if pod.Status.PodIP != "" {
					status.PodPorts = append(
						status.PodPorts,
						fmt.Sprintf("%s://%s:%s", proto, pod.Status.PodIP, servicePort.TargetPort.String()),
					)
				}
			}
		}
	}

	// Cluster IP:Port (of the service)
	if srv.Spec.ClusterIP != "" {
		status.InternalIP = append(
			status.InternalIP,
			fmt.Sprintf("%s://%s:%d", proto, srv.Spec.ClusterIP, servicePort.Port),
		)
		status.InternalDNS = append(
			status.InternalDNS,
			fmt.Sprintf("%s://%s.%s:%d", proto, srv.Name, srv.Namespace, servicePort.Port),
		)
	}

	// LoadBalancer IP:Port (of the service)
	if srv.Status.LoadBalancer.Ingress != nil {
		for _, lb := range srv.Status.LoadBalancer.Ingress {
			if lb.IP != "" {
				status.ExternalIP = append(
					status.ExternalIP,
					fmt.Sprintf("%s://%s:%d", proto, lb.IP, servicePort.Port),
				)
			}
			if lb.Hostname != "" {
				status.ExternalDNS = append(
					status.ExternalDNS,
					fmt.Sprintf("%s://%s:%d", proto, lb.Hostname, servicePort.Port),
				)
			}
		}
	}

	// External IP:Port (of the service)
	if srv.Spec.ExternalIPs != nil {
		for _, ip := range srv.Spec.ExternalIPs {
			status.ExternalIP = append(
				status.ExternalIP,
				fmt.Sprintf("%s://%s:%d", proto, ip, servicePort.Port),
			)
		}
	}

	log.Infof("Collected addresses: %+v", status)
}

// InitNooBaaClient initializes the noobaa client for making calls to the server.
func (s *System) InitNooBaaClient() error {

	if len(s.NooBaa.Status.Services.ServiceMgmt.NodePorts) == 0 {
		return fmt.Errorf("core pod port not ready yet")
	}

	nodePort := s.NooBaa.Status.Services.ServiceMgmt.NodePorts[0]
	nodeIP := nodePort[strings.Index(nodePort, "://")+3 : strings.LastIndex(nodePort, ":")]
	s.NBClient = nb.NewClient(&nb.APIRouterNodePort{
		ServiceMgmt: s.ServiceMgmt,
		NodeIP:      nodeIP,
	})
	s.NBClient.SetAuthToken(s.SecretOp.StringData["auth_token"])
	_, err := s.NBClient.ReadAuthAPI()
	return err

	// if len(s.NooBaa.Status.Services.ServiceMgmt.PodPorts) != 0 {
	// 	podPort := s.NooBaa.Status.Services.ServiceMgmt.PodPorts[0]
	// 	podIP := podPort[strings.Index(podPort, "://")+3 : strings.LastIndex(podPort, ":")]
	// 	s.NBClient = nb.NewClient(&nb.APIRouterPodPort{
	// 		ServiceMgmt: s.ServiceMgmt,
	// 		PodIP:       podIP,
	// 	})
	// 	s.NBClient.SetAuthToken(s.SecretOp.StringData["auth_token"])
	// 	return nil
	// }

}

// ReconcileSecretOp creates a new system in the noobaa server if not created yet.
func (s *System) ReconcileSecretOp() error {

	// log := s.Logger.WithName("ReconcileSecretOp")
	util.KubeCheck(s.Client, s.SecretOp)
	SecretResetStringDataFromData(s.SecretOp)

	if s.SecretOp.StringData["auth_token"] != "" {
		return nil
	}

	if s.SecretOp.StringData["email"] == "" {
		s.SecretOp.StringData["email"] = AdminAccountEmail
	}

	if s.SecretOp.StringData["password"] == "" {
		s.SecretOp.StringData["password"] = randomBase64(16)
		s.Own(s.SecretOp)
		err := s.Client.Create(s.Ctx, s.SecretOp)
		if err != nil {
			return err
		}
	}

	res, err := s.NBClient.CreateAuthAPI(nb.CreateAuthParams{
		System:   s.Request.Name,
		Role:     "admin",
		Email:    s.SecretOp.StringData["email"],
		Password: s.SecretOp.StringData["password"],
	})
	if err == nil {
		// TODO this recovery flow does not allow us to get OperatorToken like CreateSystem
		s.SecretOp.StringData["auth_token"] = res.Token
	} else {
		res, err := s.NBClient.CreateSystemAPI(nb.CreateSystemParams{
			Name:     s.Request.Name,
			Email:    s.SecretOp.StringData["email"],
			Password: s.SecretOp.StringData["password"],
		})
		if err != nil {
			return err
		}
		// TODO use res.OperatorToken after https://github.com/noobaa/noobaa-core/issues/5635
		s.SecretOp.StringData["auth_token"] = res.Token
	}
	s.NBClient.SetAuthToken(s.SecretOp.StringData["auth_token"])
	return s.Client.Update(s.Ctx, s.SecretOp)
}

// ReconcileSecretAdmin creates the admin secret
func (s *System) ReconcileSecretAdmin() error {

	log := s.Logger.WithField("func", "ReconcileSecretAdmin")

	util.KubeCheck(s.Client, s.SecretAdmin)
	SecretResetStringDataFromData(s.SecretAdmin)

	ns := s.Request.Namespace
	name := s.Request.Name
	secretAdminName := name + "-admin"

	s.SecretAdmin = &corev1.Secret{}
	err := s.GetObject(secretAdminName, s.SecretAdmin)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		log.Errorf("Failed getting admin secret: %v", err)
		return err
	}

	s.SecretAdmin = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      secretAdminName,
			Labels:    map[string]string{"app": "noobaa"},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"system":   name,
			"email":    AdminAccountEmail,
			"password": string(s.SecretOp.Data["password"]),
		},
	}

	log.Infof("listing accounts")
	res, err := s.NBClient.ListAccountsAPI()
	if err != nil {
		return err
	}
	for _, account := range res.Accounts {
		if account.Email == AdminAccountEmail {
			if len(account.AccessKeys) > 0 {
				s.SecretAdmin.StringData["AWS_ACCESS_KEY_ID"] = account.AccessKeys[0].AccessKey
				s.SecretAdmin.StringData["AWS_SECRET_ACCESS_KEY"] = account.AccessKeys[0].SecretKey
			}
		}
	}

	s.Own(s.SecretAdmin)
	return s.Client.Create(s.Ctx, s.SecretAdmin)
}

var readmeTemplate = template.Must(template.New("NooBaaSystem.Status.Readme").Parse(`

	Welcome to NooBaa!
	-----------------

	Lets get started:

	1. Connect to Management console:

		Read your mgmt console login information (email & password) from secret: "{{.SecretAdmin.Name}}".

			kubectl get secret {{.SecretAdmin.Name}} -n {{.SecretAdmin.Namespace}} -o json | jq '.data|map_values(@base64d)'

		Open the management console service - take External IP/DNS or Node Port or use port forwarding:

			kubectl port-forward -n {{.ServiceMgmt.Namespace}} service/{{.ServiceMgmt.Name}} 11443:8443 &
			open https://localhost:11443

	2. Test S3 client:

		kubectl port-forward -n {{.ServiceS3.Namespace}} service/{{.ServiceS3.Name}} 10443:443 &
		NOOBAA_ACCESS_KEY=$(kubectl get secret {{.SecretAdmin.Name}} -n {{.SecretAdmin.Namespace}} -o json | jq -r '.data.AWS_ACCESS_KEY_ID|@base64d')
		NOOBAA_SECRET_KEY=$(kubectl get secret {{.SecretAdmin.Name}} -n {{.SecretAdmin.Namespace}} -o json | jq -r '.data.AWS_SECRET_ACCESS_KEY|@base64d')
		alias s3='AWS_ACCESS_KEY_ID=$NOOBAA_ACCESS_KEY AWS_SECRET_ACCESS_KEY=$NOOBAA_SECRET_KEY aws --endpoint https://localhost:10443 --no-verify-ssl s3'
		s3 ls

`))

func (s *System) setErrorCondition(err error) {
	reason := "ReconcileFailed"
	message := fmt.Sprintf("Error while reconciling: %v", err)
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionAvailable,
		Status: corev1.ConditionUnknown,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionProgressing,
		Status: corev1.ConditionFalse,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionDegraded,
		Status: corev1.ConditionTrue,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionUpgradeable,
		Status: corev1.ConditionUnknown,
		Reason: reason,
		Message: message,
	})
}

func (s *System) setAvailableCondition(reason string, message string) {
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionAvailable,
		Status: corev1.ConditionTrue,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionProgressing,
		Status: corev1.ConditionFalse,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionDegraded,
		Status: corev1.ConditionFalse,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionUpgradeable,
		Status: corev1.ConditionTrue,
		Reason: reason,
		Message: message,
	})
}

func (s *System) setProgressingCondition(reason string, message string) {
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionAvailable,
		Status: corev1.ConditionFalse,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionProgressing,
		Status: corev1.ConditionTrue,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionDegraded,
		Status: corev1.ConditionFalse,
		Reason: reason,
		Message: message,
	})
	conditionsv1.SetStatusCondition(&s.NooBaa.Status.Conditions, conditionsv1.Condition{
		Type:   conditionsv1.ConditionUpgradeable,
		Status: corev1.ConditionFalse,
		Reason: reason,
		Message: message,
	})
}

// SetPhase updates the status phase and conditions
func (s *System) SetPhase(phase nbv1.SystemPhase) {
	s.Logger.Warnf("SetPhase %s", phase)
	s.NooBaa.Status.Phase = phase
	//if s.NooBaa.Status.Conditions == nil || len(s.NooBaa.Status.Conditions) == 0 {
	//	s.NooBaa.Status.Conditions = []nbv1.SystemCondition{{
	//		Type:    nbv1.ConditionTypePhase,
	//		Reason:  "ReconcileSetPhase",
	//		Message: "Reconcile reached phase",
	//	}}
	//}
	reason := fmt.Sprintf("%v", phase)
	message := fmt.Sprintf("%v", phase)
	if s.NooBaa.Status.Conditions == nil || len(s.NooBaa.Status.Conditions) == 0 {
		s.setAvailableCondition(reason, message)
	}

	switch phase {
		case nbv1.SystemPhaseVerifying:
			reason = "ReconcileInit"
			message = "Initializing noobaa cluster"
			s.setAvailableCondition(reason, message)
		case nbv1.SystemPhaseCreating:
			s.setProgressingCondition(reason, message)
		case nbv1.SystemPhaseWaitingToConnect:
			s.setProgressingCondition(reason, message)
		case nbv1.SystemPhaseConfiguring:
			s.setProgressingCondition(reason, message)
		case nbv1.SystemPhaseReady:
			reason = "Reconcilecompleted"
			message = "ReconcileCompleted"
			s.setAvailableCondition(reason, message)
		default:
	}
	//phaseCond := &s.NooBaa.Status.Conditions[0]
	//newPhaseStatus := nbv1.ConditionStatus(phase)
	//currstatus := phaseCond.Status
	//if currstatus != newPhaseStatus {
	//	phaseCond.LastTransitionTime = metav1.Time{Time: time.Now()}
	//}
	//phaseCond.Status = newPhaseStatus
	//phaseCond.LastProbeTime = metav1.Time{Time: time.Now()}
}

// Complete populates the noobaa status at the end of reconcile.
func (s *System) Complete() error {

	var readmeBuffer bytes.Buffer
	err := readmeTemplate.Execute(&readmeBuffer, s)
	if err != nil {
		return err
	}
	s.NooBaa.Status.Readme = readmeBuffer.String()
	s.NooBaa.Status.Accounts.Admin.SecretRef.Name = s.SecretAdmin.Name
	s.NooBaa.Status.Accounts.Admin.SecretRef.Namespace = s.SecretAdmin.Namespace
	return nil
}

// UpdateSystemStatus updates the system status in kubernetes from the memory
func (s *System) UpdateSystemStatus() error {
	log := s.Logger.WithField("func", "UpdateSystemStatus")
	log.Infof("Updating noobaa status")
	s.NooBaa.Status.ObservedGeneration = s.NooBaa.Generation
	return s.Client.Status().Update(s.Ctx, s.NooBaa)
}

// Own sets the object owner references to the noobaa system
func (s *System) Own(obj metav1.Object) {
	util.Panic(controllerutil.SetControllerReference(s.NooBaa, obj, s.Scheme))
}

// GetObject gets an object by name from the request namespace.
func (s *System) GetObject(name string, obj runtime.Object) error {
	return s.Client.Get(s.Ctx, client.ObjectKey{Namespace: s.Request.Namespace, Name: name}, obj)
}

// ReconcileObject is a generic call to reconcile a kubernetes object
// desiredFunc can be passed to modify the object before create/update.
// Currently we ignore enforcing a desired state, but it might be needed on upgrades.
func (s *System) ReconcileObject(obj runtime.Object, desiredFunc func()) error {

	kind := obj.GetObjectKind().GroupVersionKind().Kind
	objMeta, _ := meta.Accessor(obj)
	log := s.Logger.WithField("func", "ReconcileObject").WithField("kind", kind).WithField("name", objMeta.GetName())

	s.Own(objMeta)

	op, err := controllerutil.CreateOrUpdate(
		s.Ctx, s.Client, obj.(runtime.Object),
		func(obj runtime.Object) error {
			if desiredFunc != nil {
				desiredFunc()
			}
			return nil
		},
	)
	if err != nil {
		log.Errorf("ReconcileObject Failed: %v", err)
		return err
	}

	log.Infof("Done. op=%s", op)
	return nil
}

// PersistentError is an error type that tells the reconcile to avoid requeueing.
type PersistentError struct {
	E error
}

// Error function makes PersistentError implement error interface
func (e *PersistentError) Error() string { return e.E.Error() }

// assert implement error interface
var _ error = &PersistentError{}

// NewPersistentError returns a new persistent error.
func NewPersistentError(err error) *PersistentError {
	if err == nil {
		panic("NewPersistentError expects non nil error")
	}
	return &PersistentError{E: err}
}

// IsPersistentError checks if the provided error is persistent.
func IsPersistentError(err error) bool {
	_, persistent := err.(*PersistentError)
	return persistent
}

// CombineErrors takes a list of errors and combines them to one.
// Generally it will return the first non-nil error,
// but if a persistent error is found it will be returned
// instead of non-persistent errors.
func CombineErrors(errs ...error) error {
	combined := error(nil)
	for _, err := range errs {
		if err == nil {
			continue
		}
		if combined == nil {
			combined = err
			continue
		}
		if IsPersistentError(err) && !IsPersistentError(combined) {
			combined = err
		}
	}
	return combined
}

// SecretResetStringDataFromData reads the secret data into string data
// to streamline the paths that use the secret values as strings.
func SecretResetStringDataFromData(secret *corev1.Secret) {
	secret.StringData = map[string]string{}
	for key, val := range secret.Data {
		secret.StringData[key] = string(val)
	}
	secret.Data = map[string][]byte{}
}

func randomBase64(numBytes int) string {
	randomBytes := make([]byte, numBytes)
	_, err := rand.Read(randomBytes)
	util.Panic(err)
	return base64.StdEncoding.EncodeToString(randomBytes)
}

func randomHex(numBytes int) string {
	randomBytes := make([]byte, numBytes)
	_, err := rand.Read(randomBytes)
	util.Panic(err)
	return hex.EncodeToString(randomBytes)
}
