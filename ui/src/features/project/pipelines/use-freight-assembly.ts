import { useCallback } from 'react';
import { generatePath, useNavigate } from 'react-router-dom';

import { paths } from '@ui/config/paths';
import { Freight } from '@ui/gen/api/v1alpha1/generated_pb';

import { useWarehouses } from './context/warehouse-context';

export const useFreightAssembly = () => {
  const warehouses = useWarehouses();
  const navigate = useNavigate();
  
  const assembleFromFreight = useCallback((freight: Freight) => {
    const warehouse = warehouses.find((w: any) => w.metadata?.name === freight.origin?.name);
    if (!warehouse) {
      console.warn('No warehouse found for freight origin:', freight.origin?.name);
      return;
    }
    
    // Navigate to warehouse page with create-freight tab and pass source freight via state
    navigate(
      generatePath(paths.warehouse, {
        name: freight.metadata?.namespace,
        warehouseName: warehouse.metadata?.name || '',
        tab: 'create-freight'
      }),
      { 
        state: { 
          sourceFreight: freight 
        } 
      }
    );
  }, [warehouses, navigate]);
  
  const canAssemble = useCallback((freight: Freight) => {
    return warehouses.some((w: any) => w.metadata?.name === freight.origin?.name);
  }, [warehouses]);
  
  return { 
    assembleFromFreight, 
    canAssemble 
  };
};
