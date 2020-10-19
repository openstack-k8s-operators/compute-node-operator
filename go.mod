module github.com/openstack-k8s-operators/compute-node-operator

go 1.13

require (
	github.com/Masterminds/sprig v2.22.0+incompatible
	github.com/ajeddeloh/go-json v0.0.0-20200220154158-5ae607161559 // indirect
	github.com/blang/semver v3.5.1+incompatible
	github.com/coreos/ignition v0.35.0 // indirect
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32 // indirect
	github.com/go-logr/logr v0.1.0
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/openshift/cluster-api v0.0.0-20191129101638-b09907ac6668
	github.com/openshift/machine-config-operator v4.2.0-alpha.0.0.20190917115525-033375cbe820+incompatible
	github.com/openshift/sriov-network-operator v0.0.0-20200922150057-7b75a949a4d4
	github.com/openstack-k8s-operators/lib-common v0.0.0-20200910130010-129482aabaf9
	github.com/operator-framework/operator-lifecycle-manager v0.0.0-20200321030439-57b580e57e88
	github.com/pkg/errors v0.9.1
	github.com/prometheus/common v0.9.1
	github.com/vincent-petithory/dataurl v0.0.0-20191104211930-d1553a71de50 // indirect
	go4.org v0.0.0-20200411211856-f5505b9728dd // indirect
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/client-go v12.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.6.2
)

replace (
	k8s.io/client-go => k8s.io/client-go v0.18.6
)
