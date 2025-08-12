import io
import os
import re
import tempfile
from typing import Optional

import httpx
from loguru import logger

from src.config.telemetry import get_tracer
from src.utils.google_auth import get_service_account_path
from src.config import env


tracer = get_tracer("transcribe-service")


class TranscriptionError(Exception):
    def __init__(self, message: str):
        self.message = message
        super().__init__(message)


class TranscribeService:
    """Serviço de transcrição de áudio a partir de URL.

    - Valida URLs e extensões de áudio suportadas
    - Faz download para arquivo temporário (streaming)
    - Checa duração máxima permitida
    - Transcreve via Google Cloud Speech-to-Text (pt-BR)
    """

    _AUDIO_EXTENSIONS = (".mp3", ".wav", ".ogg", ".oga", ".opus", ".webm", ".m4a")

    def __init__(self):
        # Lê configuração do módulo env para manter padrão do projeto
        self._max_duration_seconds = env.TRANSCRIBE_MAX_DURATION
        allowed = env.TRANSCRIBE_ALLOWED_URLS
        self._allowed_domains = [d.strip() for d in allowed.split(",") if d.strip()]  # opcional

    def is_audio_url(self, text: str) -> bool:
        if not text or not isinstance(text, str):
            return False

        if not text.startswith(("http://", "https://")):
            return False

        if not text.lower().endswith(self._AUDIO_EXTENSIONS):
            return False

        if self._allowed_domains:
            return any(domain in text for domain in self._allowed_domains)

        return True

    def _download_audio_sync(self, url: str) -> str:
        with tracer.start_as_current_span("transcribe.download_audio") as span:
            span.set_attribute("transcribe.url", url)
            try:
                original_extension = "." + url.lower().split(".")[-1]
                # Sanitiza extensão caso venha com querystring
                if "?" in original_extension:
                    original_extension = "." + original_extension.split("?")[0].split(".")[-1]

                temp_file = tempfile.NamedTemporaryFile(delete=False, suffix=original_extension)
                temp_path = temp_file.name
                temp_file.close()

                timeout = httpx.Timeout(connect=10.0, read=120.0, write=30.0, pool=120.0)
                with httpx.Client(timeout=timeout, follow_redirects=True) as client:
                    with client.stream("GET", url) as response:
                        response.raise_for_status()
                        with open(temp_path, "wb") as f:
                            for chunk in response.iter_bytes():
                                if chunk:
                                    f.write(chunk)

                span.set_attribute("transcribe.temp_path", temp_path)
                return temp_path
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                raise TranscriptionError(f"Erro no download do áudio: {e!s}")

    def _get_audio_duration_seconds(self, audio_path: str) -> float:
        import importlib

        audio_format = audio_path.lower().split(".")[-1]
        try:
            if audio_format == "mp3":
                MP3 = importlib.import_module("mutagen.mp3").MP3
                return MP3(audio_path).info.length
            if audio_format == "wav":
                WAVE = importlib.import_module("mutagen.wave").WAVE
                return WAVE(audio_path).info.length
            if audio_format in ["ogg", "oga", "opus"]:
                try:
                    OggVorbis = importlib.import_module("mutagen.oggvorbis").OggVorbis
                    return OggVorbis(audio_path).info.length
                except Exception:
                    OggOpus = importlib.import_module("mutagen.oggopus").OggOpus
                    return OggOpus(audio_path).info.length
        except ModuleNotFoundError:
            logger.warning("mutagen não instalado; pulando checagem precisa de duração")
        except Exception as e:
            logger.warning(f"Falha ao obter duração via mutagen: {e}")

        # Outros formatos sem suporte direto ou sem mutagen: best-effort (tenta ler tamanho)
        try:
            return max(os.path.getsize(audio_path) / (16000 * 2), 0)  # heurística grosseira
        except Exception:
            return 0.0

    def _check_audio_duration(self, audio_path: str) -> None:
        with tracer.start_as_current_span("transcribe.check_duration") as span:
            try:
                duration = self._get_audio_duration_seconds(audio_path)
                span.set_attribute("transcribe.duration_seconds", float(duration))
                span.set_attribute("transcribe.max_duration_seconds", self._max_duration_seconds)
                if duration and duration > self._max_duration_seconds:
                    raise TranscriptionError(
                        f"O áudio excede a duração máxima de {self._max_duration_seconds} segundos",
                    )
            except TranscriptionError:
                raise
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                # Se não conseguir medir, não bloqueia — apenas loga
                logger.warning(f"Falha ao verificar duração do áudio: {e}")

    def _transcribe_file_sync(self, audio_path: str) -> str:
        with tracer.start_as_current_span("transcribe.transcribe_audio") as span:
            try:
                # Autenticação Google
                credentials = None
                try:
                    from google.oauth2 import service_account
                    service_account_path = get_service_account_path(env.SERVICE_ACCOUNT)
                    if service_account_path and os.path.isfile(service_account_path):
                        os.environ.setdefault("GOOGLE_APPLICATION_CREDENTIALS", service_account_path)
                        credentials = service_account.Credentials.from_service_account_file(service_account_path)
                except Exception as cred_error:
                    # ADC como fallback
                    logger.warning(f"Falha ao carregar credenciais explícitas, usando ADC: {cred_error}")

                from google.cloud import speech

                client = speech.SpeechClient(credentials=credentials) if credentials else speech.SpeechClient()

                audio_format = audio_path.lower().split(".")[-1]
                span.set_attribute("transcribe.audio_format", audio_format)

                if audio_format == "mp3":
                    encoding = speech.RecognitionConfig.AudioEncoding.MP3
                    sample_rate = 16000
                elif audio_format == "wav":
                    encoding = speech.RecognitionConfig.AudioEncoding.LINEAR16
                    sample_rate = 16000
                elif audio_format in ["ogg", "oga", "opus"]:
                    encoding = speech.RecognitionConfig.AudioEncoding.OGG_OPUS
                    sample_rate = 48000
                elif audio_format == "webm":
                    encoding = speech.RecognitionConfig.AudioEncoding.WEBM_OPUS
                    sample_rate = 48000
                else:
                    encoding = speech.RecognitionConfig.AudioEncoding.ENCODING_UNSPECIFIED
                    sample_rate = 16000

                configs = [
                    speech.RecognitionConfig(
                        encoding=encoding,
                        sample_rate_hertz=sample_rate,
                        language_code="pt-BR",
                    ),
                    speech.RecognitionConfig(
                        encoding=speech.RecognitionConfig.AudioEncoding.OGG_OPUS,
                        sample_rate_hertz=16000,
                        language_code="pt-BR",
                    ),
                    speech.RecognitionConfig(
                        encoding=speech.RecognitionConfig.AudioEncoding.ENCODING_UNSPECIFIED,
                        sample_rate_hertz=16000,
                        language_code="pt-BR",
                    ),
                    speech.RecognitionConfig(
                        encoding=speech.RecognitionConfig.AudioEncoding.LINEAR16,
                        sample_rate_hertz=16000,
                        language_code="pt-BR",
                    ),
                ]

                with io.open(audio_path, "rb") as audio_file:
                    content = audio_file.read()
                    audio = speech.RecognitionAudio(content=content)

                errors: list[str] = []
                for idx, config in enumerate(configs, start=1):
                    try:
                        response = client.recognize(config=config, audio=audio)
                        if response.results:
                            transcript = response.results[0].alternatives[0].transcript
                            span.set_attribute("transcribe.success", True)
                            span.set_attribute("transcribe.transcript_length", len(transcript))
                            return transcript
                        errors.append(f"Sem resultados (config {idx})")
                    except Exception as e:
                        errors.append(f"Erro config {idx}: {e!s}")

                logger.error("Todas as tentativas de transcrição falharam: " + "; ".join(errors))
                return "Áudio sem conteúdo reconhecível"
            except Exception as e:
                span.record_exception(e)
                span.set_attribute("error", True)
                raise TranscriptionError(f"Erro na transcrição: {e!s}")

    def transcribe_from_url_sync(self, url: str) -> str:
        """Transcreve um áudio a partir de uma URL e retorna o transcript.

        Levanta TranscriptionError em falhas críticas de download; falhas de transcrição
        retornam uma string padrão indicando ausência de conteúdo.
        """
        with tracer.start_as_current_span("transcribe.from_url") as span:
            span.set_attribute("transcribe.url", url)
            if not self.is_audio_url(url):
                raise TranscriptionError("URL inválida ou formato de áudio não suportado")

            audio_path: Optional[str] = None
            try:
                audio_path = self._download_audio_sync(url)
                # Valida arquivo gerado
                try:
                    file_size = os.path.getsize(audio_path)
                    if file_size == 0:
                        raise TranscriptionError("Arquivo de áudio vazio")
                except Exception as e:
                    raise TranscriptionError(f"Falha ao validar arquivo de áudio: {e!s}")

                self._check_audio_duration(audio_path)
                transcript = self._transcribe_file_sync(audio_path)
                return transcript
            finally:
                if audio_path and os.path.exists(audio_path):
                    try:
                        os.unlink(audio_path)
                    except Exception as e:
                        logger.warning(f"Falha ao remover arquivo temporário {audio_path}: {e}")


transcribe_service = TranscribeService()


