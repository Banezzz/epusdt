<?php
/**
 * @deprecated v1.x 使用 EzpayController 替代
 * 将在 v2.0 移除
 */
require_once __DIR__ . '/EzpayController.php';

/**
 * EpusdtController - Backward compatibility shim
 *
 * This class is kept for backward compatibility with existing Dujiaoka installations.
 * All functionality is delegated to EzpayController.
 *
 * @deprecated Use EzpayController instead
 */
class EpusdtController extends EzpayController
{
    // All methods are inherited from EzpayController
}
