import time
import math
from typing import Optional, Tuple
from loguru import logger
from src.config import env
from src.services.redis_service import sync_client, async_client


class SlidingWindowRateLimiter:
    """Proper sliding window rate limiter for Google Agent Engine API calls.
    
    This implementation maintains a constant rate by using a sliding window approach
    instead of fixed time boundaries. It prevents burst-and-wait patterns and ensures
    smooth, distributed API calls.
    """
    
    def __init__(self):
        self.enabled = env.GOOGLE_API_RATE_LIMIT_ENABLED
        self.max_requests_per_minute = env.GOOGLE_API_MAX_REQUESTS_PER_MINUTE
        self.backoff_multiplier = env.GOOGLE_API_BACKOFF_MULTIPLIER
        self.max_backoff_seconds = env.GOOGLE_API_MAX_BACKOFF_SECONDS
        self.min_backoff_seconds = env.GOOGLE_API_MIN_BACKOFF_SECONDS
        
        # Calculate the minimum interval between requests to maintain constant rate
        self.min_interval = 60.0 / self.max_requests_per_minute
        
        # Redis keys for tracking
        self.request_times_key = f"{env.APP_PREFIX}:rate_limit:google_api:request_times"
        self.backoff_key = f"{env.APP_PREFIX}:rate_limit:google_api:backoff"
        
        logger.info(f"Sliding window rate limiter initialized - Enabled: {self.enabled}, "
                   f"Max/Min: {self.max_requests_per_minute}, Min interval: {self.min_interval:.2f}s")
    
    def _get_current_time(self) -> float:
        """Get current timestamp."""
        return time.time()
    
    def _cleanup_old_requests(self) -> None:
        """Remove request timestamps older than 1 minute."""
        try:
            current_time = self._get_current_time()
            cutoff_time = current_time - 60.0
            
            # Get all request timestamps
            request_times = sync_client.zrangebyscore(
                self.request_times_key, 
                '-inf', 
                cutoff_time
            )
            
            # Remove old timestamps
            if request_times:
                sync_client.zremrangebyscore(
                    self.request_times_key, 
                    '-inf', 
                    cutoff_time
                )
                logger.debug(f"Cleaned up {len(request_times)} old request timestamps")
                
        except Exception as e:
            logger.warning(f"Failed to cleanup old requests: {e}")
    
    def _get_recent_request_count(self) -> int:
        """Get count of requests in the last minute."""
        try:
            current_time = self._get_current_time()
            cutoff_time = current_time - 60.0
            
            # Count requests in the last minute
            count = sync_client.zcount(
                self.request_times_key, 
                cutoff_time, 
                '+inf'
            )
            return count
            
        except Exception as e:
            logger.warning(f"Failed to get recent request count: {e}")
            return 0
    
    def _get_last_request_time(self) -> Optional[float]:
        """Get timestamp of the most recent request."""
        try:
            # Get the most recent request timestamp
            recent_requests = sync_client.zrange(
                self.request_times_key, 
                -1, 
                -1, 
                withscores=True
            )
            
            if recent_requests:
                return recent_requests[0][1]
            return None
            
        except Exception as e:
            logger.warning(f"Failed to get last request time: {e}")
            return None
    
    def _get_backoff_delay(self) -> float:
        """Get current backoff delay from Redis."""
        try:
            delay = sync_client.get(self.backoff_key)
            return float(delay) if delay else 0.0
        except Exception as e:
            logger.warning(f"Failed to get backoff delay: {e}")
            return 0.0
    
    def _set_backoff_delay(self, delay: float) -> None:
        """Set backoff delay in Redis with TTL."""
        try:
            # Store backoff delay for 1 hour
            sync_client.setex(self.backoff_key, 3600, str(delay))
        except Exception as e:
            logger.warning(f"Failed to set backoff delay: {e}")
    
    def _increase_backoff(self, current_delay: float) -> float:
        """Increase backoff delay exponentially."""
        new_delay = current_delay * self.backoff_multiplier
        return min(new_delay, self.max_backoff_seconds)
    
    def _reset_backoff(self) -> None:
        """Reset backoff delay when rate limit is no longer a concern."""
        try:
            sync_client.delete(self.backoff_key)
        except Exception as e:
            logger.warning(f"Failed to reset backoff delay: {e}")
    
    def check_rate_limit(self) -> Tuple[bool, float]:
        """
        Check if we should proceed with the API call.
        
        Returns:
            tuple: (should_proceed, delay_seconds)
        """
        if not self.enabled:
            return True, 0.0
        
        try:
            # Clean up old request timestamps
            self._cleanup_old_requests()
            
            current_time = self._get_current_time()
            recent_count = self._get_recent_request_count()
            last_request_time = self._get_last_request_time()
            current_backoff = self._get_backoff_delay()
            
            # If we're at or over the limit, don't proceed
            if recent_count >= self.max_requests_per_minute:
                # Calculate how long to wait until we can make another request
                # We need to wait until the oldest request is more than 1 minute old
                oldest_request_time = current_time - 60.0
                wait_time = 60.0 - (current_time - oldest_request_time)
                
                logger.warning(f"Rate limit exceeded: {recent_count}/{self.max_requests_per_minute} "
                             f"requests in last minute. Waiting {wait_time:.2f}s")
                
                return False, max(wait_time, current_backoff)
            
            # If we're approaching the limit (80% threshold), apply backoff
            threshold = int(self.max_requests_per_minute * 0.8)
            if recent_count >= threshold:
                delay = current_backoff
                logger.info(f"Approaching rate limit: {recent_count}/{self.max_requests_per_minute}. "
                          f"Applying backoff: {delay:.2f}s")
                return True, delay
            
            # Check if we need to respect minimum interval between requests
            if last_request_time is not None:
                time_since_last = current_time - last_request_time
                if time_since_last < self.min_interval:
                    wait_time = self.min_interval - time_since_last
                    logger.debug(f"Respecting minimum interval: waiting {wait_time:.2f}s")
                    return True, wait_time
            
            # If we're well under limits, reduce backoff
            if recent_count < threshold * 0.5 and current_backoff > 0:
                new_backoff = max(current_backoff / self.backoff_multiplier, self.min_backoff_seconds)
                self._set_backoff_delay(new_backoff)
                logger.info(f"Reducing backoff delay to {new_backoff:.2f}s")
            
            return True, 0.0
            
        except Exception as e:
            logger.error(f"Error checking rate limit: {e}")
            # On error, allow the request but log the issue
            return True, 0.0
    
    def record_api_call(self) -> None:
        """Record that an API call was made."""
        if not self.enabled:
            return
        
        try:
            current_time = self._get_current_time()
            
            # Add current timestamp to the sorted set
            sync_client.zadd(self.request_times_key, {str(current_time): current_time})
            
            # Set TTL on the key (2 minutes to ensure cleanup)
            sync_client.expire(self.request_times_key, 120)
            
            logger.debug(f"Recorded API call at {current_time}")
            
        except Exception as e:
            logger.warning(f"Failed to record API call: {e}")
    
    def record_rate_limit_hit(self) -> None:
        """Record that we hit a rate limit and increase backoff."""
        if not self.enabled:
            return
        
        current_backoff = self._get_backoff_delay()
        new_backoff = self._increase_backoff(current_backoff)
        
        if new_backoff > current_backoff:
            self._set_backoff_delay(new_backoff)
            logger.warning(f"Increased backoff delay to {new_backoff:.2f}s due to rate limit hit")
    
    def wait_if_needed(self) -> float:
        """
        Wait if rate limiting is needed.
        
        Returns:
            float: Actual delay time in seconds
        """
        if not self.enabled:
            return 0.0
        
        should_proceed, delay_seconds = self.check_rate_limit()
        
        if not should_proceed or delay_seconds > 0:
            logger.info(f"Rate limiting: waiting {delay_seconds:.2f} seconds")
            time.sleep(delay_seconds)
            return delay_seconds
        
        return 0.0
    
    async def wait_if_needed_async(self) -> float:
        """Async version of wait_if_needed."""
        if not self.enabled:
            return 0.0
        
        should_proceed, delay_seconds = self.check_rate_limit()
        
        if not should_proceed or delay_seconds > 0:
            logger.info(f"Rate limiting: waiting {delay_seconds:.2f} seconds")
            import asyncio
            await asyncio.sleep(delay_seconds)
            return delay_seconds
        
        return 0.0
    
    def get_current_stats(self) -> dict:
        """Get current rate limiting statistics for monitoring."""
        try:
            recent_count = self._get_recent_request_count()
            last_request_time = self._get_last_request_time()
            current_backoff = self._get_backoff_delay()
            
            return {
                "enabled": self.enabled,
                "max_requests_per_minute": self.max_requests_per_minute,
                "min_interval_seconds": self.min_interval,
                "recent_request_count": recent_count,
                "last_request_time": last_request_time,
                "current_backoff_seconds": current_backoff,
                "remaining_requests": max(0, self.max_requests_per_minute - recent_count)
            }
        except Exception as e:
            logger.warning(f"Failed to get current stats: {e}")
            return {"error": str(e)}


# Global instance
rate_limiter = SlidingWindowRateLimiter()
