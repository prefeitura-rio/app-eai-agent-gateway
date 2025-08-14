import base64
import json
import os
import tempfile
from pathlib import Path
from typing import Optional

from loguru import logger


def decode_service_account_base64(base64_credentials: str) -> str | None:
    """
    Decodifica credenciais de service account em base64 e salva em arquivo temporário.
    
    Args:
        base64_credentials: String em base64 contendo as credenciais JSON
        
    Returns:
        Caminho para o arquivo temporário com as credenciais ou None se houver erro
    """
    if not base64_credentials:
        return None

    try:
        # Decodifica o base64
        decoded_bytes = base64.b64decode(base64_credentials)
        credentials_json = decoded_bytes.decode('utf-8')

        # Valida se é um JSON válido
        json.loads(credentials_json)

        # Cria arquivo temporário
        temp_dir = Path(tempfile.gettempdir())
        temp_file = temp_dir / "google_service_account.json"

        # Escreve as credenciais no arquivo
        with open(temp_file, 'w', encoding='utf-8') as f:
            f.write(credentials_json)

        logger.info(f"Service account credentials decoded and saved to {temp_file}")
        return str(temp_file)

    except Exception as e:
        logger.error(f"Failed to decode service account base64: {e}")
        return None


def get_service_account_path(service_account_env: str) -> str | None:
    """
    Obtém o caminho para as credenciais da service account.
    
    Suporta:
    - Caminho direto para arquivo JSON
    - Email da service account (para usar ADC)
    - Credenciais em base64
    
    Args:
        service_account_env: Valor da variável de ambiente SERVICE_ACCOUNT
        
    Returns:
        Caminho para arquivo de credenciais ou None para usar ADC
    """
    if not service_account_env:
        logger.warning("SERVICE_ACCOUNT not configured, will use Application Default Credentials")
        return None

    # Se contém '@', é um email da service account
    if '@' in service_account_env:
        logger.info(f"Using service account email: {service_account_env}")
        return service_account_env

    # Se é um caminho para arquivo existente
    if os.path.isfile(service_account_env):
        logger.info(f"Using service account file: {service_account_env}")
        return service_account_env

    # Tenta decodificar como base64
    decoded_path = decode_service_account_base64(service_account_env)
    if decoded_path:
        # Configura a variável de ambiente para que o Google Auth encontre as credenciais
        os.environ['GOOGLE_APPLICATION_CREDENTIALS'] = decoded_path
        logger.info(f"Set GOOGLE_APPLICATION_CREDENTIALS to {decoded_path}")
        return decoded_path

    logger.error(f"Invalid SERVICE_ACCOUNT format: {service_account_env[:50]}...")
    return None
