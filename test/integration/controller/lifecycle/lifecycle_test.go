// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"fmt"
	"time"

	"github.com/Masterminds/semver/v3"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/utils"
	"github.com/gardener/gardener/pkg/utils/managedresources"
	"github.com/gardener/gardener/pkg/utils/test"
	. "github.com/gardener/gardener/pkg/utils/test/matchers"
	versionutils "github.com/gardener/gardener/pkg/utils/version"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gardener/gardener-extension-shoot-rsyslog-relp/pkg/apis/rsyslog"
)

var _ = Describe("Lifecycle controller tests", func() {
	var (
		authModeName  rsyslog.AuthMode = "name"
		tlsLibOpenSSL rsyslog.TLSLib   = "openssl"

		rsyslogConfigurationCleanerDaemonsetYaml = func(pspDisabled bool) string {
			return `# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0

---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: rsyslog-relp-configuration-cleaner
  namespace: kube-system
  labels:
    app.kubernetes.io/name: rsyslog-relp-configuration-cleaner
    app.kubernetes.io/instance: rsyslog-relp-configuration-cleaner
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: rsyslog-relp-configuration-cleaner
      app.kubernetes.io/instance: rsyslog-relp-configuration-cleaner
  template:
    metadata:
      labels:
        app.kubernetes.io/name: rsyslog-relp-configuration-cleaner
        app.kubernetes.io/instance: rsyslog-relp-configuration-cleaner
    spec:
      securityContext:
        seccompProfile:
          type: RuntimeDefault
      priorityClassName: gardener-shoot-system-700
      containers:
      - name: pause-container
        image: registry.k8s.io/pause:3.9
        imagePullPolicy: IfNotPresent
      initContainers:
      - name: rsyslog-configuration-cleaner
        image: eu.gcr.io/gardener-project/3rd/alpine:3.18.4
        imagePullPolicy: IfNotPresent
        command:
        - "sh"
        - "-c"
        - |
          if [[ -f /host/etc/systemd/system/rsyslog-configurator.service ]]; then
            chroot /host /bin/bash -c 'systemctl disable rsyslog-configurator; systemctl stop rsyslog-configurator; rm -f /etc/systemd/system/rsyslog-configurator.service'
          fi

          if [[ -f /host/etc/audit/plugins.d/syslog.conf ]]; then
            sed -i 's/yes/no/g' /host/etc/audit/plugins.d/syslog.conf
          fi

          if [[ -d /host/etc/audit/rules.d.original ]]; then
            if [[ -d /host/etc/audit/rules.d ]]; then
              rm -rf /host/etc/audit/rules.d
            fi
            mv /host/etc/audit/rules.d.original /host/etc/audit/rules.d
            chroot /host /bin/bash -c 'if systemctl list-unit-files auditd.service > /dev/null; then augenrules --load; systemctl restart auditd; fi'
          fi

          if [[ -f /host/etc/rsyslog.d/60-audit.conf ]]; then
            rm -f /host/etc/rsyslog.d/60-audit.conf
            chroot /host /bin/bash -c 'if systemctl list-unit-files rsyslog.service > /dev/null; then systemctl restart rsyslog; fi'
          fi

          if [[ -d /host/var/lib/rsyslog-relp-configurator ]]; then
            rm -rf /host/var/lib/rsyslog-relp-configurator
          fi
        resources:
          requests:
            memory: 8Mi
            cpu: 2m
          limits:
            memory: 16Mi
        volumeMounts:
        - name: host-root-volume
          mountPath: /host
          readOnly: false` + stringBasedOnCondition(!pspDisabled, `
      serviceAccountName: rsyslog-relp-configuration-cleaner`, ``) + `
      hostPID: true
      volumes:
      - name: host-root-volume
        hostPath:
          path: /`
		}

		auditdConfigMapYaml = `# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0
kind: ConfigMap
apiVersion: v1
metadata:
  name: rsyslog-relp-configurator-auditd-config
  namespace: kube-system
data:
  00-base-config.rules: |
    ## First rule - delete all
    -D
    ## Increase the buffers to survive stress events.
    ## Make this bigger for busy systems
    -b 8192
    ## This determine how long to wait in burst of events
    --backlog_wait_time 60000
    ## Set failure mode to syslog
    -f 1
  10-privilege-escalation.rules: |
    -a exit,always -F arch=b64 -S setuid -S setreuid -S setgid -S setregid -F auid>0 -F auid!=-1 -F key=privilege_escalation
    -a exit,always -F arch=b64 -S execve -S execveat -F euid=0 -F auid>0 -F auid!=-1 -F key=privilege_escalation
  11-privileged-special.rules: |
    -a exit,always -F arch=b64 -S mount -S mount_setattr -S umount2 -S mknod -S mknodat -S chroot -F auid!=-1 -F key=privileged_special
  12-system-integrity.rules: |
    -a exit,always -F dir=/boot -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/etc -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/bin -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/sbin -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/lib -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/lib64 -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/usr -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/opt -F perm=wa -F key=system_integrity
    -a exit,always -F dir=/root -F perm=wa -F key=system_integrity
  configured-by-rsyslog-relp-configurator: |
    # The files in this directory are managed by the shoot-rsyslog-relp extension
    # The original files were moved to /etc/audit/rules.d.original`

		rsyslogConfigMapYaml = func(tlsEnabled bool, projectName, shootName string, shootUID types.UID) string {
			return `# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0

apiVersion: v1
kind: ConfigMap
metadata:
  name: rsyslog-relp-configurator-config
  namespace: kube-system
data:
  rsyslog-configurator.service: |
    [Unit]
    Description=rsyslog configurator daemon
    Documentation=https://github.com/gardener/gardener-extension-shoot-rsyslog-relp
    [Service]
    Type=simple
    Restart=always
    RestartSec=15
    ExecStart=/var/lib/rsyslog-relp-configurator/configure-rsyslog.sh
    [Install]
    WantedBy=multi-user.target

  configure-rsyslog.sh: |
    #!/bin/bash

    function configure_auditd() {
      if [[ ! -d /etc/audit/rules.d.original ]] && [[ -d /etc/audit/rules.d ]]; then
        mv /etc/audit/rules.d /etc/audit/rules.d.original
      fi

      if [[ ! -d /etc/audit/rules.d ]]; then
        mkdir -p /etc/audit/rules.d
      fi
      if ! diff -rq /var/lib/rsyslog-relp-configurator/audit/rules.d /etc/audit/rules.d ; then
        rm -rf /etc/audit/rules.d/*
        cp -L /var/lib/rsyslog-relp-configurator/audit/rules.d/* /etc/audit/rules.d/
        if [[ -f /etc/audit/plugins.d/syslog.conf ]]; then
          sed -i 's/no/yes/g' /etc/audit/plugins.d/syslog.conf
        fi
        augenrules --load
        systemctl restart auditd
      fi
    }

    function configure_rsyslog() {
      if [[ ! -f /etc/rsyslog.d/60-audit.conf ]] || ! diff -rq /var/lib/rsyslog-relp-configurator/rsyslog.d/60-audit.conf /etc/rsyslog.d/60-audit.conf ; then
        cp -fL /var/lib/rsyslog-relp-configurator/rsyslog.d/60-audit.conf /etc/rsyslog.d/60-audit.conf
        systemctl restart rsyslog
      elif ! systemctl is-active --quiet rsyslog.service ; then
        # Ensure that the rsyslog service is running.
        systemctl start rsyslog.service
      fi
    }

    if systemctl list-unit-files auditd.service > /dev/null; then
      echo "Configuring auditd.service ..."
      configure_auditd
    else
      echo "auditd.service is not installed, skipping configuration"
    fi

    # Make sure that the syslog.service symlink which points to the rsyslog.service unit is created before attempting
    # to configure rsyslog to ensure proper startup of the rsyslog.service.
    # TODO(plkokanov): due to an issue on gardenlinux, syslog.service is missing: https://github.com/gardenlinux/gardenlinux/issues/1864.
    # This means that for gardenlinux we have to skip the check if syslog.service exists for now.
    # As rsyslog.service comes preinstalled on gardenlinux, this should not lead to configuration problems.
    if grep -q gardenlinux /etc/os-release || \
      systemctl list-unit-files syslog.service > /dev/null && \
      systemctl list-unit-files rsyslog.service > /dev/null; then
      echo "Configuring rsyslog.service ..."
      configure_rsyslog
    else
      echo "rsyslog.service and syslog.service are not installed, skipping configuration"
    fi

  process_rsyslog_pstats.sh: |
    #!/bin/bash

    set -e

    output_dir="/tmp"

    while [[ $# -gt 0 ]]; do
      case "$1" in
        -o|--output_dir)
          shift; output_dir="${1:-$output_dir}"
          ;;
        *)
          echo "Unknown argument: $1"
          exit 1
          ;;
      esac
      shift
    done

    process_json() {
      local json=$1

      echo $json | \
        jq -r '
          ([to_entries[] | select(.value|type=="string") | "\(.key)=\"\(.value)\""] | join(",")) as $labels
          | to_entries[] | select(.value|type=="number")
          | "rsyslog_pstat_\(.key | sub("\\.";"_")){\($labels)} \(.value)"
        ' || { logger -p error -t  process_rsyslog_pstats.sh  "Error processing JSON: $json"; exit 1; }
    }

    process_line() {
      local line=$1
      local json

      json=$(echo $line | sed -n 's/.*rsyslogd-pstats: //p') || { logger -p error -t  process_rsyslog_pstats.sh  "Error extracting JSON from line: $line"; return 1; }
      process_json "$json"
    }

    add_comments() {
      local prefix=$1
      local help_and_type=()

      case $prefix in
        *"rsyslog_pstat_submitted")
          help_and_type=('Number of messages submitted' 'counter')
          ;;
        *"processed")
          help_and_type=('Number of messages processed' 'counter')
          ;;
        *"rsyslog_pstat_failed")
          help_and_type=('Number of messages failed' 'counter')
          ;;
        *"rsyslog_pstat_suspended")
          help_and_type=('Number of times suspended' 'counter')
          ;;
        *"rsyslog_pstat_suspended_duration")
          help_and_type=('Time spent suspended' 'counter')
          ;;
        *"rsyslog_pstat_resumed")
          help_and_type=('Number of times resumed' 'counter')
          ;;
        *"rsyslog_pstat_utime")
          help_and_type=('User time used in microseconds' 'counter')
          ;;
        *"rsyslog_pstat_stime")
          help_and_type=('System time used in microsends' 'counter')
          ;;
        *"rsyslog_pstat_maxrss")
          help_and_type=('Maximum resident set size' 'gauge')
          ;;
        *"rsyslog_pstat_minflt")
          help_and_type=('Total minor faults' 'counter')
          ;;
        *"rsyslog_pstat_majflt")
          help_and_type=('Total major faults' 'counter')
          ;;
        *"rsyslog_pstat_inblock")
          help_and_type=('Filesystem input operations' 'counter')
          ;;
        *"rsyslog_pstat_oublock")
          help_and_type=('Filesystem output operations' 'counter')
          ;;
        *"rsyslog_pstat_nvcsw")
          help_and_type=('Voluntary context switches' 'counter')
          ;;
        *"rsyslog_pstat_nivcsw")
          help_and_type=('Involuntary context switches' 'counter')
          ;;
        *"rsyslog_pstat_openfiles")
          help_and_type=('Number of open files' 'counter')
          ;;
        *"rsyslog_pstat_size")
          help_and_type=('Messages currently in queue' 'gauge')
          ;;
        *"rsyslog_pstat_enqueued")
          help_and_type=('Total messages enqueued' 'counter')
          ;;
        *"rsyslog_pstat_full")
          help_and_type=('Times queue was full' 'counter')
          ;;
        *"rsyslog_pstat_discarded_full")
          help_and_type=('Messages discarded due to queue being full' 'counter')
          ;;
        *"rsyslog_pstat_discarded_nf")
          help_and_type=('Messages discarded when queue not full' 'counter')
          ;;
        *"rsyslog_pstat_maxqsize")
          help_and_type=('Maximum size queue has reached' 'gauge')
          ;;
      esac

      if [ ${#help_and_type[@]} -eq 0 ]; then
        return 0
      fi

      comments+="# HELP ${prefix} ${help_and_type[0]}.\n"
      comments+="# TYPE ${prefix} ${help_and_type[1]}"
      echo "$comments"
    }

    # Create output directory if it does not exist.
    mkdir -p "$output_dir"

    declare -a lines
    output_file="$output_dir/rsyslog_pstats.prom"
    output=""
    prev_prefix=""

    while IFS= read -r line; do
      if [[ $line == *"rsyslogd-pstats: BEGIN"* ]]; then
        # Start of a new batch, clear the lines array and the output string.
        lines=()
        output=""
        prev_prefix=""
      elif [[ $line == *"rsyslogd-pstats: END"* ]]; then
        # End of a batch, sort the lines array, aggregate the output and write it to a file.
        IFS=$'\n' lines=($(sort <<<"${lines[*]}"))
        for line in "${lines[@]}"; do
          prefix=$(echo "$line" | cut -d'{' -f1) || { logger -p error -t  process_rsyslog_pstats.sh  "Error extracting prefix from line: $line"; exit 1; }
          if [[ "$prefix" != "$prev_prefix" ]]; then
            comment="$(add_comments "$prefix")"
            if [[ -z $comment ]]; then
              # If no help and type comment was added, then the metric is not known. Hence we do not add it
              # to the output.
              continue
            fi
            output+="$comment\n"
            prev_prefix="$prefix"
          fi
          output+="$line\n"
        done
        # Writing to the output prom file has to be done with an atomic operation. This is why we first write to a temporary file
        # and then we move/rename the temporary file to the actual output file.
        echo -e "$output" >> "$output_file.tmp" || { logger -p error -t  process_rsyslog_pstats.sh  "Error writing to temp output file"; exit 1; }
        mv "$output_file.tmp" "$output_file"
      else
        processed_line=$(process_line "$line") || { logger -p error -t  process_rsyslog_pstats.sh  "Error processing line: $line"; exit 1; }
        lines+=("$processed_line")
      fi
    done

  60-audit.conf: |
    template(name="SyslogForwarderTemplate" type="list") {
      constant(value=" ")
      constant(value="` + projectName + `")
      constant(value=" ")
      constant(value="` + shootName + `")
      constant(value=" ")
      constant(value="` + string(shootUID) + `")
      constant(value=" ")
      property(name="hostname")
      constant(value=" ")
      property(name="pri")
      constant(value=" ")
      property(name="syslogtag")
      constant(value=" ")
      property(name="timestamp" dateFormat="rfc3339")
      constant(value=" ")
      property(name="procid")
      constant(value=" ")
      property(name="msgid")
      constant(value=" ")
      property(name="msg")
      constant(value=" ")
    }

    module(
      load="omrelp"` + stringBasedOnCondition(tlsEnabled, `
      tls.tlslib="openssl"`, "") + `
    )

    module(load="omprog")
    module(
      load="impstats"
      interval="60"
      format="json"
      resetCounters="off"
      ruleset="process_stats"
      bracketing="on"
    )

    ruleset(name="process_stats") {
      action(
        type="omprog"
        name="to_pstats_processor"
        binary="/var/lib/rsyslog-relp-configurator/process_rsyslog_pstats.sh -o /var/lib/node-exporter/textfile-collector"
      )
    }

    ruleset(name="relp_action_ruleset") {
      action(
        name="rsyslog-relp"
        type="omrelp"
        target="localhost"
        port="10250"
        Template="SyslogForwarderTemplate"` + stringBasedOnCondition(tlsEnabled, `
        tls="on"
        tls.caCert="/var/lib/rsyslog-relp-configurator/tls/ca.crt"
        tls.myCert="/var/lib/rsyslog-relp-configurator/tls/tls.crt"
        tls.myPrivKey="/var/lib/rsyslog-relp-configurator/tls/tls.key"
        tls.authmode="name"
        tls.permittedpeer=["rsyslog-server.foo","rsyslog-server.foo.bar"]`, "") + `
      )
    }

    if $programname == ["systemd","audisp-syslog"] and $syslogseverity <= 5 then {
      call relp_action_ruleset
      stop
    }
    if $programname == ["kubelet"] and $syslogseverity <= 7 then {
      call relp_action_ruleset
      stop
    }
    if $syslogseverity <= 2 then {
      call relp_action_ruleset
      stop
    }

    input(type="imuxsock" Socket="/run/systemd/journal/syslog")`
		}

		rsyslogTlsSecretYaml = func(tlsEnabled bool) string {
			if !tlsEnabled {
				return `# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0`
			}

			return `# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0
apiVersion: v1
kind: Secret
metadata:
  name: rsyslog-relp-configurator-tls
  namespace: kube-system
type: Opaque
data:
  ca.crt: ` + utils.EncodeBase64([]byte("ca")) + `
  tls.crt: ` + utils.EncodeBase64([]byte("crt")) + `
  tls.key: ` + utils.EncodeBase64([]byte("key"))
		}

		rsyslogConfiguratorDaemonsetYaml = func(tlsEnabled, pspDisabled bool, rsyslogConfigMap, auditdConfigMap, tlsSecret string) string {
			return `# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0

apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: rsyslog-relp-configurator
  namespace: kube-system
  labels:
    app.kubernetes.io/name: rsyslog-relp-configurator
    app.kubernetes.io/instance: rsyslog-relp-configurator
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: rsyslog-relp-configurator
      app.kubernetes.io/instance: rsyslog-relp-configurator
  template:
    metadata:
      annotations:` + stringBasedOnCondition(tlsEnabled, `
        checksum/rsyslog-relp-configurator-tls: `+utils.ComputeSHA256Hex([]byte(tlsSecret)), "") + `
        checksum/rsyslog-relp-configurator-config: ` + utils.ComputeSHA256Hex([]byte(rsyslogConfigMap)) + `
        checksum/rsyslog-relp-configurator-auditd-config: ` + utils.ComputeSHA256Hex([]byte(auditdConfigMap)) + `
      labels:
        app.kubernetes.io/name: rsyslog-relp-configurator
        app.kubernetes.io/instance: rsyslog-relp-configurator
    spec:
      securityContext:
        seccompProfile:
          type: RuntimeDefault
      priorityClassName: gardener-shoot-system-700
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        imagePullPolicy: IfNotPresent
      initContainers:
      - name: rsyslog-relp-configurator
        image: eu.gcr.io/gardener-project/3rd/alpine:3.18.4
        imagePullPolicy: IfNotPresent
        command:
        - "sh"
        - "-c"
        - |
          mkdir -p /host/var/lib/rsyslog-relp-configurator/audit/rules.d
          cp -fL /var/lib/rsyslog-relp-configurator/audit/rules.d/* /host/var/lib/rsyslog-relp-configurator/audit/rules.d/
          mkdir -p /host/var/lib/rsyslog-relp-configurator/rsyslog.d
          cp -fL /var/lib/rsyslog-relp-configurator/config/60-audit.conf /host/var/lib/rsyslog-relp-configurator/rsyslog.d/60-audit.conf` + stringBasedOnCondition(tlsEnabled, `
          mkdir -p /host/var/lib/rsyslog-relp-configurator/tls
          cp -fL /var/lib/rsyslog-relp-configurator/tls/* /host/var/lib/rsyslog-relp-configurator/tls/`, "") + `
          cp -fL /var/lib/rsyslog-relp-configurator/config/configure-rsyslog.sh /host/var/lib/rsyslog-relp-configurator/configure-rsyslog.sh
          chmod +x /host/var/lib/rsyslog-relp-configurator/configure-rsyslog.sh
          cp -fL /var/lib/rsyslog-relp-configurator/config/rsyslog-configurator.service /host/etc/systemd/system/rsyslog-configurator.service
          chroot /host /bin/bash -c "systemctl enable rsyslog-configurator; systemctl start rsyslog-configurator"
          cp -fL /var/lib/rsyslog-relp-configurator/config/process_rsyslog_pstats.sh /host/var/lib/rsyslog-relp-configurator/process_rsyslog_pstats.sh
          chmod +x /host/var/lib/rsyslog-relp-configurator/process_rsyslog_pstats.sh
        resources:
          requests:
            memory: 8Mi
            cpu: 2m
          limits:
            memory: 16Mi
        volumeMounts:` + stringBasedOnCondition(tlsEnabled, `
        - name: rsyslog-relp-configurator-tls-volume
          mountPath: /var/lib/rsyslog-relp-configurator/tls`,
				"") + `
        - name: rsyslog-relp-configurator-config-volume
          mountPath: /var/lib/rsyslog-relp-configurator/config
        - name: auditd-config-volume
          mountPath: /var/lib/rsyslog-relp-configurator/audit/rules.d
        - name: host-root-volume
          mountPath: /host
          readOnly: false` + stringBasedOnCondition(!pspDisabled, `
      serviceAccountName: rsyslog-relp-configurator`, ``) + `
      hostPID: true
      tolerations:
      - effect: NoSchedule
        operator: Exists
      - effect: NoExecute
        operator: Exists
      volumes:` + stringBasedOnCondition(tlsEnabled, `
      - name: rsyslog-relp-configurator-tls-volume
        secret:
          secretName: rsyslog-relp-configurator-tls`,
				"") + `
      - name: rsyslog-relp-configurator-config-volume
        configMap:
          name: rsyslog-relp-configurator-config
      - name: auditd-config-volume
        configMap:
          name: rsyslog-relp-configurator-auditd-config
      - name: host-root-volume
        hostPath:
          path: /`
		}

		rsyslogRelpPSPYaml = func(pspDisabled bool, name string) string {
			return stringBasedOnCondition(
				pspDisabled,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0`,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0
---
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  annotations:
    seccomp.security.alpha.kubernetes.io/defaultProfileName: 'runtime/default'
    seccomp.security.alpha.kubernetes.io/allowedProfileNames: 'runtime/default'
  name: gardener.kube-system.`+name+`
spec:
  hostPID: true
  volumes:
  - hostPath`+stringBasedOnCondition(name == "rsyslog-relp-configurator", `
  - secret
  - configMap`, ``)+`
  allowedHostPaths:
  - pathPrefix: /
  readOnlyRootFilesystem: true
  runAsUser:
    rule: RunAsAny
  seLinux:
    rule: RunAsAny
  supplementalGroups:
    rule: RunAsAny
  fsGroup:
    rule: RunAsAny`)
		}

		rsyslogRelpPSPClusterRoleYaml = func(pspDisabled bool, name string) string {
			return stringBasedOnCondition(
				pspDisabled,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0`,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: gardener.cloud:psp:kube-system:`+name+`
rules:
- apiGroups:
  - policy
  - extensions
  resourceNames:
  - gardener.kube-system.`+name+`
  resources:
  - podsecuritypolicies
  verbs:
  - use`)
		}

		rsyslogRelpPSPServiceAccountYaml = func(pspDisabled bool, name string) string {
			return stringBasedOnCondition(
				pspDisabled,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0`,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: `+name+`
  namespace: kube-system
  labels:
    app.kubernetes.io/name: `+name+`
    app.kubernetes.io/instance: `+name+`
automountServiceAccountToken: false`)
		}

		rsyslogRelpPSPRoleBindingYaml = func(pspDisabled bool, name string) string {
			return stringBasedOnCondition(
				pspDisabled,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0`,
				`# SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: gardener.cloud:psp:`+name+`
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: gardener.cloud:psp:kube-system:`+name+`
subjects:
- kind: ServiceAccount
  name: `+name+`
  namespace: kube-system`)
		}

		cluster  *extensionsv1alpha1.Cluster
		shoot    *gardencorev1beta1.Shoot
		shootUID types.UID

		extensionProviderConfig *rsyslog.RsyslogRelpConfig
		extensionResource       *extensionsv1alpha1.Extension
	)

	BeforeEach(func() {
		shootName = "shoot-" + utils.ComputeSHA256Hex([]byte(uuid.NewUUID()))[:8]
		projectName = "test-" + utils.ComputeSHA256Hex([]byte(uuid.NewUUID()))[:5]
		shootUID = uuid.NewUUID()
		shootTechnicalID = fmt.Sprintf("shoot--%s--%s", projectName, shootName)

		By("Create test Namespace")
		shootSeedNamespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: shootTechnicalID,
			},
		}
		Expect(testClient.Create(ctx, shootSeedNamespace)).To(Succeed())
		log.Info("Created Namespace for test", "namespaceName", shootSeedNamespace.Name)

		DeferCleanup(func() {
			By("Delete test Namespace")
			Expect(client.IgnoreNotFound(testClient.Delete(ctx, shootSeedNamespace))).To(Succeed())
		})

		shoot = &gardencorev1beta1.Shoot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      shootName,
				Namespace: fmt.Sprintf("garden-%s", projectName),
				UID:       shootUID,
			},
			Spec: gardencorev1beta1.ShootSpec{
				Provider: gardencorev1beta1.Provider{
					Workers: []gardencorev1beta1.Worker{{Name: "worker"}},
				},
				Kubernetes: gardencorev1beta1.Kubernetes{
					Version: "1.27.2",
				},
				Resources: []gardencorev1beta1.NamedResourceReference{
					{
						Name: "rsyslog-tls",
						ResourceRef: v1.CrossVersionObjectReference{
							Kind: "Secret",
							Name: "rsyslog-tls",
						},
					},
				},
			},
		}

		extensionProviderConfig = &rsyslog.RsyslogRelpConfig{
			Target: "localhost",
			Port:   10250,
			LoggingRules: []rsyslog.LoggingRule{
				{
					Severity:     5,
					ProgramNames: []string{"systemd", "audisp-syslog"},
				},
				{
					Severity:     7,
					ProgramNames: []string{"kubelet"},
				},
				{
					Severity: 2,
				},
			},
		}
	})

	JustBeforeEach(func() {
		By("Create Cluster")
		cluster = &extensionsv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: shootTechnicalID,
			},
			Spec: extensionsv1alpha1.ClusterSpec{
				Shoot: runtime.RawExtension{
					Object: shoot,
				},
				Seed: runtime.RawExtension{
					Object: &gardencorev1beta1.Seed{},
				},
				CloudProfile: runtime.RawExtension{
					Object: &gardencorev1beta1.CloudProfile{},
				},
			},
		}

		Expect(testClient.Create(ctx, cluster)).To(Succeed())
		log.Info("Created cluster for test", "cluster", client.ObjectKeyFromObject(cluster))

		By("Ensure manager cache observes cluster creation")
		Eventually(func() error {
			return mgrClient.Get(ctx, client.ObjectKeyFromObject(cluster), &extensionsv1alpha1.Cluster{})
		}).Should(Succeed())

		DeferCleanup(func() {
			By("Delete Cluster")
			Expect(client.IgnoreNotFound(testClient.Delete(ctx, cluster))).To(Succeed())
		})

		extensionResource = &extensionsv1alpha1.Extension{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shoot-rsyslog-relp",
				Namespace: shootSeedNamespace.Name,
			},
			Spec: extensionsv1alpha1.ExtensionSpec{
				DefaultSpec: extensionsv1alpha1.DefaultSpec{
					ProviderConfig: &runtime.RawExtension{
						Object: extensionProviderConfig,
					},
					Type: "shoot-rsyslog-relp",
				},
			},
		}

		By("Create shoot-rsyslog-relp Extension Resource")
		Expect(testClient.Create(ctx, extensionResource)).To(Succeed())
		log.Info("Created shoot-rsyslog-tls extension resource", "extension", client.ObjectKeyFromObject(extensionResource))

		DeferCleanup(func() {
			By("Delete shoot-rsyslog-relp Extension Resource")
			Expect(testClient.Delete(ctx, extensionResource)).To(Or(Succeed(), BeNotFoundError()))
		})
	})

	var test = func() {
		It("should properly reconcile the extension resource", func() {
			DeferCleanup(test.WithVars(
				&managedresources.IntervalWait, time.Millisecond,
			))

			tlsEnabled := extensionProviderConfig.TLS != nil && extensionProviderConfig.TLS.Enabled
			pspDisabled := versionutils.ConstraintK8sGreaterEqual125.Check(semver.MustParse(shoot.Spec.Kubernetes.Version))

			managedResource := &resourcesv1alpha1.ManagedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "extension-shoot-rsyslog-relp-shoot",
					Namespace: shootSeedNamespace.Name,
				},
			}
			managedResourceSecret := &corev1.Secret{}

			By("Verify that managed resource is created correctly")
			rsyslogConfigMap := rsyslogConfigMapYaml(tlsEnabled, projectName, shootName, shootUID)
			rsyslogTlsSecret := rsyslogTlsSecretYaml(tlsEnabled)
			Eventually(func(g Gomega) {
				g.Expect(mgrClient.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(Succeed())

				managedResourceSecret = &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      managedResource.Spec.SecretRefs[0].Name,
						Namespace: managedResource.Namespace,
					},
				}

				g.Expect(mgrClient.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(Succeed())
				g.Expect(managedResourceSecret.Type).To(Equal(corev1.SecretTypeOpaque))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_auditd-config.yaml"])).To(Equal(auditdConfigMapYaml))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_configmap.yaml"])).To(Equal(rsyslogConfigMap))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_tls.yaml"])).To(Equal(rsyslogTlsSecret))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_daemonset.yaml"])).To(Equal(rsyslogConfiguratorDaemonsetYaml(tlsEnabled, pspDisabled, rsyslogConfigMap, auditdConfigMapYaml, rsyslogTlsSecret)))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_clusterrole-psp.yaml"])).To(Equal(rsyslogRelpPSPClusterRoleYaml(pspDisabled, "rsyslog-relp-configurator")))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_psp.yaml"])).To(Equal(rsyslogRelpPSPYaml(pspDisabled, "rsyslog-relp-configurator")))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_rolebinding-psp.yaml"])).To(Equal(rsyslogRelpPSPRoleBindingYaml(pspDisabled, "rsyslog-relp-configurator")))
				g.Expect(string(managedResourceSecret.Data["rsyslog-relp-configurator_templates_serviceaccount.yaml"])).To(Equal(rsyslogRelpPSPServiceAccountYaml(pspDisabled, "rsyslog-relp-configurator")))
			}).Should(Succeed())

			By("Delete shoot-rsyslog-relp Extension Resource")
			Expect(testClient.Delete(ctx, extensionResource)).To(Succeed())

			By("Verify that managed resource used for configuration gets deleted")
			Eventually(func(g Gomega) {
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(BeNotFoundError())
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(BeNotFoundError())
			}).Should(Succeed())

			configCleanerManagedResource := &resourcesv1alpha1.ManagedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "extension-shoot-rsyslog-relp-configuration-cleaner-shoot",
					Namespace: shootSeedNamespace.Name,
				},
			}
			configCleanerResourceSecret := &corev1.Secret{}

			By("Verify that managed resource used for configuration cleanup gets created")
			Eventually(func(g Gomega) {
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(configCleanerManagedResource), configCleanerManagedResource)).To(Succeed())

				configCleanerResourceSecret = &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      configCleanerManagedResource.Spec.SecretRefs[0].Name,
						Namespace: configCleanerManagedResource.Namespace,
					},
				}

				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(configCleanerResourceSecret), configCleanerResourceSecret)).To(Succeed())
				g.Expect(configCleanerResourceSecret.Type).To(Equal(corev1.SecretTypeOpaque))
				g.Expect(string(configCleanerResourceSecret.Data["rsyslog-relp-configuration-cleaner_templates_daemonset.yaml"])).To(Equal(rsyslogConfigurationCleanerDaemonsetYaml(pspDisabled)))
				g.Expect(string(configCleanerResourceSecret.Data["rsyslog-relp-configuration-cleaner_templates_clusterrole-psp.yaml"])).To(Equal(rsyslogRelpPSPClusterRoleYaml(pspDisabled, "rsyslog-relp-configuration-cleaner")))
				g.Expect(string(configCleanerResourceSecret.Data["rsyslog-relp-configuration-cleaner_templates_psp.yaml"])).To(Equal(rsyslogRelpPSPYaml(pspDisabled, "rsyslog-relp-configuration-cleaner")))
				g.Expect(string(configCleanerResourceSecret.Data["rsyslog-relp-configuration-cleaner_templates_rolebinding-psp.yaml"])).To(Equal(rsyslogRelpPSPRoleBindingYaml(pspDisabled, "rsyslog-relp-configuration-cleaner")))
				g.Expect(string(configCleanerResourceSecret.Data["rsyslog-relp-configuration-cleaner_templates_serviceaccount.yaml"])).To(Equal(rsyslogRelpPSPServiceAccountYaml(pspDisabled, "rsyslog-relp-configuration-cleaner")))
			}).Should(Succeed())

			By("Ensure that managed resource used for configuration cleanup does not get deleted immediately")
			Consistently(func(g Gomega) {
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(configCleanerManagedResource), managedResource)).To(Succeed())
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(configCleanerResourceSecret), managedResourceSecret)).To(Succeed())
			}).Should(Succeed())

			By("Set managed resource used for configuration cleanup to healthy")
			patch := client.MergeFrom(configCleanerManagedResource.DeepCopy())
			configCleanerManagedResource.Status.Conditions = append(configCleanerManagedResource.Status.Conditions, []gardencorev1beta1.Condition{
				{
					Type:               resourcesv1alpha1.ResourcesApplied,
					Status:             gardencorev1beta1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					LastUpdateTime:     metav1.Now(),
				},
				{
					Type:               resourcesv1alpha1.ResourcesHealthy,
					Status:             gardencorev1beta1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					LastUpdateTime:     metav1.Now(),
				},
			}...)
			configCleanerManagedResource.Status.ObservedGeneration = 1
			Expect(testClient.Status().Patch(ctx, configCleanerManagedResource, patch)).To(Succeed())

			By("Verify that managed resource used for configuration cleanup gets deleted")
			Eventually(func(g Gomega) {
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(configCleanerManagedResource), configCleanerManagedResource)).To(BeNotFoundError())
				g.Expect(testClient.Get(ctx, client.ObjectKeyFromObject(configCleanerResourceSecret), configCleanerResourceSecret)).To(BeNotFoundError())
			}).Should(Succeed())
		})
	}

	Context("when TLS is not enabled", func() {
		test()
	})

	Context("when PSP is enabled", func() {
		BeforeEach(func() {
			shoot.Spec.Kubernetes.Version = "1.24.8"
		})
		test()
	})

	Context("when TLS is enabled", func() {
		BeforeEach(func() {
			extensionProviderConfig.TLS = &rsyslog.TLS{
				Enabled:             true,
				SecretReferenceName: pointer.String("rsyslog-tls"),
				AuthMode:            &authModeName,
				TLSLib:              &tlsLibOpenSSL,
				PermittedPeer:       []string{"rsyslog-server.foo", "rsyslog-server.foo.bar"},
			}

			rsyslogSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ref-rsyslog-tls",
					Namespace: shootSeedNamespace.Name,
				},
				Data: map[string][]byte{
					"ca":  []byte("ca"),
					"crt": []byte("crt"),
					"key": []byte("key"),
				},
			}

			By("Create rsyslog-tls Secret")
			Expect(testClient.Create(ctx, rsyslogSecret)).To(Succeed())
			log.Info("Created rsyslog-tls secret", "secret", client.ObjectKeyFromObject(rsyslogSecret))

			DeferCleanup(func() {
				By("Delete rsyslog-tls Secret")
				Expect(testClient.Delete(ctx, rsyslogSecret)).To(Or(Succeed(), BeNotFoundError()))
			})
		})

		test()
	})
})

func stringBasedOnCondition(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}
