package provisioninglog

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/rancher/pkg/controllers/dashboard/clusterindex"
	"github.com/rancher/rancher/pkg/controllers/provisioningv2/rke2"
	provisioningcontrollers "github.com/rancher/rancher/pkg/generated/controllers/provisioning.cattle.io/v1"
	"github.com/rancher/rancher/pkg/wrangler"
	corev1controllers "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	provisioningLogName = "provisioning-log"
	maxLen              = 10000
)

var (
	clusterRegexp = regexp.MustCompile("^c-m-[a-z0-9]{8}$")
)

func Register(ctx context.Context, wContext *wrangler.Context) {
	h := &handler{
		configMapsCache: wContext.Core.ConfigMap().Cache(),
		configMaps:      wContext.Core.ConfigMap(),
		clusterCache:    wContext.Provisioning.Cluster().Cache(),
	}
	wContext.Core.Namespace().OnChange(ctx, "prov-log-namespace", h.OnNamespace)
	wContext.Core.ConfigMap().OnChange(ctx, "prov-log-configmap", h.OnConfigMap)
}

type handler struct {
	configMapsCache corev1controllers.ConfigMapCache
	configMaps      corev1controllers.ConfigMapController
	clusterCache    provisioningcontrollers.ClusterCache
}

func (h *handler) OnConfigMap(key string, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	if cm == nil {
		return nil, nil
	}
	if cm.Name != provisioningLogName || (!clusterRegexp.MatchString(cm.Namespace) && cm.Namespace != "local") {
		return cm, nil
	}
	provCluster, err := h.clusterCache.GetByIndex(clusterindex.ClusterV1ByClusterV3Reference, cm.Namespace)
	if apierrors.IsNotFound(err) || len(provCluster) == 0 {
		return cm, nil
	} else if err != nil {
		return cm, err
	}
	if provCluster[0].Spec.RKEConfig == nil {
		return cm, nil
	}

	h.configMaps.EnqueueAfter(cm.Namespace, cm.Name, 2*time.Second)
	return h.recordMessage(provCluster[0], cm)
}

func appendLog(error bool, oldLog, log string) string {
	if len(oldLog) > maxLen {
		oldLog = oldLog[:maxLen]
		oldLog = strings.TrimRightFunc(oldLog, func(r rune) bool {
			return r != '\n'
		})
	}
	prefix := " [INFO ] "
	if error {
		prefix = " [ERROR] "
	}
	return oldLog + time.Now().Format(time.RFC3339) + prefix + log + "\n"
}

func (h *handler) recordMessage(provCluster *provv1.Cluster, cm *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	msg := rke2.Provisioned.GetMessage(provCluster)
	error := rke2.Provisioned.IsFalse(provCluster)
	done := rke2.Provisioned.IsTrue(provCluster)

	if done && msg == "" && provCluster.Status.Ready {
		msg = "provisioning done"
	}

	if msg == "" {
		return cm, nil
	}

	last := cm.Data["last"]
	if msg == last {
		return cm, nil
	}

	cm = cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}

	cm.Data["log"] = appendLog(error, cm.Data["log"], msg)
	cm.Data["last"] = msg
	return h.configMaps.Update(cm)
}

func (h *handler) OnNamespace(key string, ns *corev1.Namespace) (*corev1.Namespace, error) {
	if ns == nil || !ns.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	if !clusterRegexp.MatchString(ns.Name) {
		return ns, nil
	}
	if _, err := h.configMapsCache.Get(ns.Name, provisioningLogName); apierrors.IsNotFound(err) {
		_, err := h.configMaps.Create(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      provisioningLogName,
				Namespace: ns.Name,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("creating %s for %s: %w", provisioningLogName, ns.Name, err)
		}
	}
	return ns, nil
}
