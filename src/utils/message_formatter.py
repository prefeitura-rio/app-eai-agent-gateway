import hashlib
import uuid
from datetime import UTC, datetime, timezone
from typing import Any, Dict, List, Optional, Union

from langchain_core.load.dump import dumpd
from langchain_core.messages import BaseMessage

from src.utils.md_to_wpp import markdown_to_whatsapp


class LangGraphMessageFormatter:
    """
    Formatador especializado para mensagens do LangGraph Agent Engine.
    
    Converte mensagens do formato LangChain/LangGraph para o formato padronizado
    do gateway, incluindo funcionalidades como:
    - Session ID determinístico baseado em tempo
    - Timestamps e tempo entre mensagens
    - Serialização automática de objetos BaseMessage
    - Suporte opcional para formato WhatsApp
    - Estatísticas de uso agregadas
    """

    def __init__(self, thread_id: str | None = None):
        self.thread_id = thread_id
        self.reset_state()

    def reset_state(self):
        """Reseta o estado interno do formatador"""
        self.current_session_id = None
        self.current_session_start_time = None
        self.last_message_timestamp = None
        self.tool_call_to_name = {}
        self.current_step_id = f"step-{uuid.uuid4()}"

    def serialize_message(self, message: BaseMessage) -> dict[str, Any]:
        """
        Serializa mensagem BaseMessage usando dumpd do langchain.
        
        Args:
            message: Objeto BaseMessage do LangChain
            
        Returns:
            Dict com dados serializados da mensagem
        """
        raw = dumpd(message)

        # Adicionar usage_metadata se existir no response_metadata
        response_metadata = getattr(message, "response_metadata", None)
        if response_metadata and "usage_metadata" in response_metadata:
            raw["usage_metadata"] = response_metadata["usage_metadata"]

        return raw

    def generate_deterministic_session_id(self, timestamp_str: str, thread_id: str | None = None) -> str:
        """
        Gera um session_id determinístico baseado no timestamp e thread_id.
        
        Args:
            timestamp_str: String do timestamp da primeira mensagem da sessão
            thread_id: ID do thread (opcional)
            
        Returns:
            Session ID determinístico
        """
        base_string = f"{timestamp_str}_{thread_id or self.thread_id or 'unknown'}"
        hash_object = hashlib.md5(base_string.encode())
        hash_hex = hash_object.hexdigest()
        return f"{hash_hex[:16]}"

    def should_create_new_session(self, time_since_last_message: float | None, timeout_seconds: int | None) -> bool:
        """
        Determina se deve criar uma nova sessão baseado no tempo desde a última mensagem.
        
        Args:
            time_since_last_message: Tempo em segundos desde a última mensagem
            timeout_seconds: Timeout da sessão em segundos
            
        Returns:
            True se deve criar nova sessão, False caso contrário
        """
        if time_since_last_message is None or timeout_seconds is None:
            return False

        return time_since_last_message > timeout_seconds

    def parse_timestamp(self, timestamp_str: str | None) -> datetime | None:
        """
        Converte string de timestamp para datetime object.
        
        Args:
            timestamp_str: String do timestamp
            
        Returns:
            Objeto datetime ou None se não for possível parsear
        """
        if not timestamp_str:
            return None
        try:
            # Remove 'Z' se presente e substitui por '+00:00'
            if timestamp_str.endswith("Z"):
                timestamp_str = timestamp_str[:-1] + "+00:00"
            return datetime.fromisoformat(timestamp_str)
        except:
            return None

    def calculate_time_since_last_message(self, current_timestamp: str | None) -> float | None:
        """
        Calcula o tempo em segundos desde a última mensagem.
        
        Args:
            current_timestamp: Timestamp da mensagem atual
            
        Returns:
            Tempo em segundos ou None se não for possível calcular
        """
        if not current_timestamp or not self.last_message_timestamp:
            return None

        current_dt = self.parse_timestamp(current_timestamp)
        last_dt = self.parse_timestamp(self.last_message_timestamp)

        if current_dt and last_dt:
            return (current_dt - last_dt).total_seconds()

        return None

    def update_session_state(self, message_timestamp: str | None, time_since_last_message: float | None,
                           session_timeout_seconds: int | None):
        """
        Atualiza o estado da sessão baseado no timestamp e timeout.
        
        Args:
            message_timestamp: Timestamp da mensagem atual
            time_since_last_message: Tempo desde a última mensagem
            session_timeout_seconds: Timeout da sessão
        """
        if session_timeout_seconds is None:
            # Para API: session_id sempre None
            self.current_session_id = None
        else:
            # Para histórico completo: verificar se deve criar nova sessão
            if self.current_session_id is None or self.should_create_new_session(
                time_since_last_message, session_timeout_seconds
            ):
                # Nova sessão: usar o timestamp atual como base para gerar o ID determinístico
                self.current_session_start_time = message_timestamp
                self.current_session_id = self.generate_deterministic_session_id(
                    self.current_session_start_time, self.thread_id
                )

        # Atualizar o último timestamp de mensagem
        if message_timestamp:
            self.last_message_timestamp = message_timestamp

    def extract_message_metadata(self, kwargs: dict[str, Any]) -> dict[str, Any]:
        """
        Extrai metadados da mensagem baseado no tipo.
        
        Args:
            kwargs: Dados kwargs da mensagem serializada
            
        Returns:
            Dict com metadados extraídos
        """
        msg_type = kwargs.get("type")
        response_metadata = kwargs.get("response_metadata", {})
        usage_md = response_metadata.get("usage_metadata", {})

        if msg_type in ["human", "tool"]:
            # user_message e tool_return_message: campos null
            return {
                "model_name": None,
                "finish_reason": None,
                "avg_logprobs": None,
                "usage_metadata": None,
            }
        else:
            # assistant_message e tool_call_message: campos com dados
            return {
                "model_name": response_metadata.get("model_name", ""),
                "finish_reason": response_metadata.get("finish_reason", ""),
                "avg_logprobs": response_metadata.get("avg_logprobs"),
                "usage_metadata": {
                    "prompt_token_count": usage_md.get("prompt_token_count", 0),
                    "candidates_token_count": usage_md.get("candidates_token_count", 0),
                    "total_token_count": usage_md.get("total_token_count", 0),
                    "thoughts_token_count": usage_md.get("thoughts_token_count", 0),
                    "cached_content_token_count": usage_md.get("cached_content_token_count", 0),
                },
            }

    def create_base_message_dict(self, kwargs: dict[str, Any], message_timestamp: str | None,
                               time_since_last_message: float | None, metadata: dict[str, Any]) -> dict[str, Any]:
        """
        Cria o dicionário base comum a todos os tipos de mensagem.
        
        Args:
            kwargs: Dados kwargs da mensagem
            message_timestamp: Timestamp da mensagem
            time_since_last_message: Tempo desde a última mensagem
            metadata: Metadados extraídos
            
        Returns:
            Dict com campos base da mensagem
        """
        original_id = kwargs.get("id", "").replace("run--", "")

        return {
            "id": original_id or f"message-{uuid.uuid4()}",
            "date": message_timestamp,
            "session_id": self.current_session_id,
            "time_since_last_message": time_since_last_message,
            "name": None,
            "otid": str(uuid.uuid4()),
            "sender_id": None,
            "step_id": self.current_step_id,
            "is_err": None,
            **metadata,
        }

    def process_human_message(self, kwargs: dict[str, Any], base_dict: dict[str, Any]) -> dict[str, Any]:
        """Processa mensagem do tipo human."""
        return {
            **base_dict,
            "message_type": "user_message",
            "content": kwargs.get("content", ""),
        }

    def process_ai_message(self, kwargs: dict[str, Any], base_dict: dict[str, Any],
                          use_whatsapp_format: bool) -> list[dict[str, Any]]:
        """Processa mensagem do tipo AI, podendo gerar múltiplas mensagens."""
        messages = []
        content = kwargs.get("content", "")
        tool_calls = kwargs.get("tool_calls", [])

        response_metadata = kwargs.get("response_metadata", {})
        usage_md = response_metadata.get("usage_metadata", {})
        output_details = usage_md.get("output_token_details") or {}
        reasoning_tokens = output_details.get("reasoning", 0) or 0

        if tool_calls:
            # Construir mapeamento de tool_call_id para nome da ferramenta
            for tc in tool_calls:
                tool_call_id = tc.get("id")
                tool_name = tc.get("name", "unknown")
                if tool_call_id:
                    self.tool_call_to_name[tool_call_id] = tool_name

            # Adicionar reasoning message se houver reasoning tokens
            if reasoning_tokens > 0:
                messages.append({
                    **base_dict,
                    "id": base_dict["id"] or f"{uuid.uuid4()}",
                    "message_type": "reasoning_message",
                    "source": "reasoner_model",
                    "reasoning": f"Processando chamada para ferramenta {tool_calls[0].get('name', 'unknown')}",
                    "signature": None,
                })

            # Processar cada tool call
            for tc in tool_calls:
                messages.append(self._process_tool_call(tc, base_dict))

        elif content:
            # Adicionar reasoning message se houver reasoning tokens
            if reasoning_tokens > 0:
                messages.append({
                    **base_dict,
                    "id": f"{base_dict['id'] or uuid.uuid4()}",
                    "message_type": "reasoning_message",
                    "source": "reasoner_model",
                    "reasoning": "Processando resposta para o usuário",
                    "signature": None,
                })

            # Adicionar assistant message
            messages.append({
                **base_dict,
                "message_type": "assistant_message",
                "content": (
                    markdown_to_whatsapp(content)
                    if use_whatsapp_format
                    else content
                ),
            })

        return messages

    def _process_tool_call(self, tool_call: dict[str, Any], base_dict: dict[str, Any]) -> dict[str, Any]:
        """Processa uma tool call individual."""
        tool_call_id = tool_call.get("id", str(uuid.uuid4()))

        args = tool_call.get("args", {})

        return {
            **base_dict,
            "id": f"{tool_call_id}",
            "message_type": "tool_call_message",
            "tool_call": {
                "name": tool_call.get("name", "unknown"),
                "arguments": args,
                "tool_call_id": tool_call_id,
            },
        }

    def process_tool_message(self, kwargs: dict[str, Any], base_dict: dict[str, Any]) -> dict[str, Any]:
        """Processa mensagem do tipo tool."""
        status = "error" if (kwargs.get("status") == "error") else "success"
        tool_call_id = kwargs.get("tool_call_id", "")

        # Usar o mapeamento para obter o nome da ferramenta
        tool_name = (
            self.tool_call_to_name.get(tool_call_id)
            or kwargs.get("name")
            or "unknown_tool"
        )

        # Manter content original - pode ser string ou objeto complexo
        tool_content = kwargs.get("content", "")

        # Atualizar step_id após tool return
        self.current_step_id = f"step-{uuid.uuid4()}"

        return {
            **base_dict,
            "id": base_dict["id"] or f"tool-return-{tool_call_id}",
            "name": tool_name,
            "message_type": "tool_return_message",
            "is_err": status == "error",
            "tool_return": tool_content,
            "status": status,
            "tool_call_id": tool_call_id,
            "stdout": None,
            "stderr": tool_content if status == "error" else None,
        }

    def calculate_usage_statistics(self, messages_to_process: list[dict[str, Any]]) -> dict[str, Any]:
        """
        Calcula estatísticas de uso agregadas.
        
        Args:
            messages_to_process: Lista de mensagens processadas
            
        Returns:
            Dict com estatísticas de uso
        """
        input_tokens = 0
        output_tokens = 0
        total_tokens = 0
        model_names = set()

        for msg in messages_to_process:
            kwargs = msg.get("kwargs", {})
            response_metadata = kwargs.get("response_metadata", {})
            usage_md = response_metadata.get("usage_metadata", {})

            # Mapear campos corretos do Google AI
            input_tokens += int(usage_md.get("prompt_token_count", 0) or 0)
            output_tokens += int(usage_md.get("candidates_token_count", 0) or 0)
            total_tokens += int(usage_md.get("total_token_count", 0) or 0)

            # Coletar model_names
            model_name = response_metadata.get("model_name")
            if model_name:
                model_names.add(model_name)

        return {
            "message_type": "usage_statistics",
            "completion_tokens": output_tokens,
            "prompt_tokens": input_tokens,
            "total_tokens": total_tokens or (input_tokens + output_tokens),
            "step_count": len({m.get("step_id") for m in self.processed_messages if m.get("step_id")}),
            "steps_messages": None,
            "run_ids": None,
            "agent_id": self.thread_id,
            "processed_at": datetime.now(UTC).isoformat(),
            "status": "done",
            "model_names": list(model_names),
        }

    def format_messages(
        self,
        messages: list[BaseMessage | dict[str, Any]],
        thread_id: str | None = None,
        session_timeout_seconds: int | None = None,
        use_whatsapp_format: bool = True,
    ) -> dict[str, Any]:
        """
        Converte uma lista de mensagens para o formato Gateway.

        Args:
            messages: Lista de mensagens (BaseMessage ou dict já serializados)
            thread_id: ID do thread/agente (opcional, sobrescreve o da instância)
            session_timeout_seconds: Tempo limite em segundos para nova sessão
            use_whatsapp_format: Define se deve usar o markdown_to_whatsapp

        Returns:
            Dict no formato Gateway com status, data, mensagens e estatísticas de uso
        """
        # Usar thread_id fornecido ou o da instância
        if thread_id:
            self.thread_id = thread_id

        # Resetar estado para nova formatação
        self.reset_state()
        self.processed_messages = []

        # Serializar mensagens se necessário
        messages_to_process = []
        for msg in messages:
            if isinstance(msg, BaseMessage):
                serialized_msg = self.serialize_message(msg)
                messages_to_process.append(serialized_msg)
            elif hasattr(msg, 'model_dump'):
                # Objeto Pydantic - converter para dict
                messages_to_process.append(msg.model_dump())
            elif hasattr(msg, 'dict'):
                # Objeto com método dict() - converter para dict
                messages_to_process.append(msg.dict())
            elif hasattr(msg, '__dict__'):
                # Objeto genérico - tentar converter usando __dict__
                msg_dict = {}
                for key, value in msg.__dict__.items():
                    if hasattr(value, 'model_dump'):
                        msg_dict[key] = value.model_dump()
                    elif hasattr(value, 'dict'):
                        msg_dict[key] = value.dict()
                    else:
                        msg_dict[key] = value
                messages_to_process.append(msg_dict)
            else:
                messages_to_process.append(msg)

        # Processar cada mensagem
        for msg in messages_to_process:
            # Garantir que msg é um dicionário
            if not isinstance(msg, dict):
                # Se ainda não for um dict, tentar converter
                if hasattr(msg, 'model_dump'):
                    msg = msg.model_dump()
                elif hasattr(msg, 'dict'):
                    msg = msg.dict()
                elif hasattr(msg, '__dict__'):
                    msg = {k: v for k, v in msg.__dict__.items()}
                else:
                    # Pular mensagem que não pode ser processada
                    continue
            
            # Detectar se é formato LangChain serializado ou formato direto
            if "kwargs" in msg:
                # Formato LangChain serializado (BaseMessage)
                kwargs = msg.get("kwargs", {})
                msg_type = kwargs.get("type")
                additional_kwargs = kwargs.get("additional_kwargs", {})
                message_timestamp = additional_kwargs.get("timestamp", None)
            else:
                # Formato direto (dict simples) - adaptar para o formato esperado
                if msg.get("type") in ["human", "ai", "tool"]:
                    # Formato Google Agent Engine
                    kwargs = msg
                    msg_type = msg.get("type")
                    message_timestamp = msg.get("timestamp")
                elif msg.get("message_type"):
                    # Formato Letta - processar diretamente sem conversão
                    letta_message_type = msg.get("message_type")
                    
                    # Calcular tempo entre mensagens
                    message_timestamp = msg.get("date")
                    time_since_last_message = self.calculate_time_since_last_message(message_timestamp)
                    
                    # Atualizar estado da sessão
                    self.update_session_state(message_timestamp, time_since_last_message, session_timeout_seconds)
                    
                    # Criar mensagem preservando os dados do Letta
                    processed_msg = {
                        "id": msg.get("id", f"message-{uuid.uuid4()}"),
                        "date": message_timestamp,
                        "session_id": self.current_session_id,
                        "time_since_last_message": time_since_last_message,
                        "name": msg.get("name"),
                        "otid": msg.get("otid", str(uuid.uuid4())),
                        "sender_id": msg.get("sender_id"),
                        "step_id": msg.get("step_id", self.current_step_id),
                        "is_err": msg.get("is_err"),
                        "message_type": letta_message_type,
                    }
                    
                    # Adicionar campos específicos por tipo
                    if letta_message_type == "reasoning_message":
                        processed_msg.update({
                            "source": msg.get("source"),
                            "reasoning": msg.get("reasoning"),
                            "signature": msg.get("signature"),
                            "model_name": None,
                            "finish_reason": None,
                            "avg_logprobs": None,
                            "usage_metadata": None,
                        })
                    elif letta_message_type == "assistant_message":
                        content = msg.get("content", "")
                        processed_msg.update({
                            "content": markdown_to_whatsapp(content) if use_whatsapp_format else content,
                            "model_name": msg.get("model_name"),
                            "finish_reason": msg.get("finish_reason"),
                            "avg_logprobs": msg.get("avg_logprobs"),
                            "usage_metadata": msg.get("usage_metadata"),
                        })
                    elif letta_message_type == "tool_call_message":
                        processed_msg.update({
                            "tool_call": msg.get("tool_call"),
                            "model_name": msg.get("model_name"),
                            "finish_reason": msg.get("finish_reason"),
                            "avg_logprobs": msg.get("avg_logprobs"),
                            "usage_metadata": msg.get("usage_metadata"),
                        })
                    elif letta_message_type == "tool_return_message":
                        processed_msg.update({
                            "tool_return": msg.get("tool_return", msg.get("content")),
                            "status": "error" if msg.get("is_err") else "success",
                            "tool_call_id": msg.get("tool_call_id"),
                            "stdout": msg.get("stdout"),
                            "stderr": msg.get("stderr"),
                            "model_name": None,
                            "finish_reason": None,
                            "avg_logprobs": None,
                            "usage_metadata": None,
                        })
                    elif letta_message_type == "user_message":
                        processed_msg.update({
                            "content": msg.get("content", ""),
                            "model_name": None,
                            "finish_reason": None,
                            "avg_logprobs": None,
                            "usage_metadata": None,
                        })
                    
                    self.processed_messages.append(processed_msg)
                    continue  # Pular o processamento normal
                else:
                    # Formato desconhecido, pular
                    continue

            # Calcular tempo entre mensagens
            time_since_last_message = self.calculate_time_since_last_message(message_timestamp)

            # Atualizar estado da sessão
            self.update_session_state(message_timestamp, time_since_last_message, session_timeout_seconds)

            # Extrair metadados
            metadata = self.extract_message_metadata(kwargs)

            # Criar base da mensagem
            base_dict = self.create_base_message_dict(kwargs, message_timestamp, time_since_last_message, metadata)

            # Processar baseado no tipo
            if msg_type == "human":
                processed_msg = self.process_human_message(kwargs, base_dict)
                self.processed_messages.append(processed_msg)

            elif msg_type == "ai":
                processed_msgs = self.process_ai_message(kwargs, base_dict, use_whatsapp_format)
                self.processed_messages.extend(processed_msgs)

            elif msg_type == "tool":
                processed_msg = self.process_tool_message(kwargs, base_dict)
                self.processed_messages.append(processed_msg)

        # Calcular estatísticas de uso
        usage_stats = self.calculate_usage_statistics(messages_to_process)
        self.processed_messages.append(usage_stats)

        return {
            "status": "completed",
            "data": {
                "messages": self.processed_messages,
            },
        }


# Função de conveniência para manter compatibilidade
def to_gateway_format(
    messages: list[BaseMessage | dict[str, Any]],
    thread_id: str | None = None,
    session_timeout_seconds: int | None = None,
    use_whatsapp_format: bool = True,
) -> dict[str, Any]:
    """
    Função de conveniência que mantém a interface anterior.
    
    Args:
        messages: Lista de mensagens (BaseMessage ou dict já serializados)
        thread_id: ID do thread/agente (opcional)
        session_timeout_seconds: Tempo limite em segundos para nova sessão
        use_whatsapp_format: Define se deve usar o markdown_to_whatsapp
        
    Returns:
        Dict no formato Gateway com status, data, mensagens e estatísticas de uso
    """
    formatter = LangGraphMessageFormatter(thread_id=thread_id)
    return formatter.format_messages(
        messages=messages,
        thread_id=thread_id,
        session_timeout_seconds=session_timeout_seconds,
        use_whatsapp_format=use_whatsapp_format,
    )
