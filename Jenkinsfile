pipeline {
    agent any  // Utiliza un agente genérico; cámbialo si es necesario

    environment {
        DOCKER_CLI_EXPERIMENTAL = 'enabled'
        IMAGE_NAME = 'mi-imagen'  // Nombre de la imagen Docker
        DOCKER_REGISTRY = 'docker.io'  // Registro Docker, puedes usar Docker Hub u otro
        DOCKER_TAG = "latest1.0"  // El tag de la imagen
    }

    stages {
        stage('Preparar Docker Buildx') {
            steps {
                script {
                    // Verifica que Docker y Buildx están instalados
                    sh 'docker --version'
                    sh 'docker buildx version'

                    // Crea un nuevo builder usando Buildx
                    sh 'docker buildx create --use'
                }
                echo 'Preparando Buildx...'
            }
        }

        stage('Construir Imagen') {
            steps {
                script {
                    // Construcción de la imagen Docker con soporte multi-plataforma y push al registro
                    sh """
                        docker buildx build --platform linux/amd64,linux/arm64 -t $DOCKER_REGISTRY/$IMAGE_NAME:$DOCKER_TAG . --push
                    """
                }
                echo 'Imagen construida y subida al registro...'
            }
        }

        stage('Iniciar Sesión en Docker Registry') {
            steps {
                script {
                    // Inicia sesión en Docker (solo si es necesario empujar la imagen)
                    withCredentials([usernamePassword(credentialsId: 'docker-hub-credentials', usernameVariable: 'DOCKER_USERNAME', passwordVariable: 'DOCKER_PASSWORD')]) {
                        sh """
                            docker login -u \$DOCKER_USERNAME -p \$DOCKER_PASSWORD
                        """
                    }
                }
            }
        }

        stage('Deploy') {
            steps {
                echo 'Desplegando la aplicación...'
            }
        }
    }

    post {
        always {
            // Realiza cualquier acción adicional al finalizar el pipeline
            echo 'Pipeline completado.'
        }
        success {
            echo 'Imagen construida y subida correctamente.'
        }
        failure {
            echo 'Hubo un error en el pipeline.'
        }
    }
}
