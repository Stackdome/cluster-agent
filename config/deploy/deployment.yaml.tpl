apiVersion: apps/v1
kind: Deployment
metadata:
  name: stackdome-operator-manager
  namespace: stackdome-control-plane
  labels:
    app.kubernetes.io/name: stackdome-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: stackdome-operator
  template:
    metadata:
      labels:
        app.kubernetes.io/name: stackdome-operator
    spec:
      serviceAccountName: stackdome-operator
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: manager
        image: controller-image
        args:
        - --leader-elect
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
          initialDelaySeconds: 15
          periodSeconds: 20
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
